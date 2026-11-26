package assistant

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"

	"github.com/towertail/towertail/server/config"
)

// anthropicServer replays a fixture stream and keeps the request bodies.
type anthropicServer struct {
	*httptest.Server
	mu      sync.Mutex
	bodies  []string
	headers []http.Header
}

func newAnthropicServer(t *testing.T, fixture string) *anthropicServer {
	t.Helper()
	stream, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal(err)
	}
	s := &anthropicServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.bodies = append(s.bodies, string(b))
		s.headers = append(s.headers, r.Header.Clone())
		s.mu.Unlock()
		if r.URL.Path != "/v1/messages" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write(stream)
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *anthropicServer) last() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.bodies[len(s.bodies)-1]
}

// jv reads a JSON value by path. A missing path gives an empty jv.
type jv struct{ v any }

func parse(s string) jv {
	var v any
	_ = json.Unmarshal([]byte(s), &v)
	return jv{v}
}

func (j jv) get(path ...any) jv {
	v := j.v
	for _, p := range path {
		switch k := p.(type) {
		case string:
			m, _ := v.(map[string]any)
			v = m[k]
		case int:
			a, _ := v.([]any)
			if k >= len(a) {
				return jv{}
			}
			v = a[k]
		}
	}
	return jv{v}
}

func (j jv) exists() bool { return j.v != nil }
func (j jv) str() string  { s, _ := j.v.(string); return s }
func (j jv) num() int     { f, _ := j.v.(float64); return int(f) }
func (j jv) on() bool     { b, _ := j.v.(bool); return b }
func (j jv) raw() string  { b, _ := json.Marshal(j.v); return string(b) }
func (j jv) arr() []jv {
	var out []jv
	a, _ := j.v.([]any)
	for _, v := range a {
		out = append(out, jv{v})
	}
	return out
}

// find gives the first item of an array with key equal to val.
func (j jv) find(key, val string) jv {
	for _, it := range j.arr() {
		if it.get(key).str() == val {
			return it
		}
	}
	return jv{}
}

func collect(t *testing.T, m Model, r Request) []Part {
	t.Helper()
	var parts []Part
	for p, err := range m.Stream(t.Context(), r) {
		if err != nil {
			t.Fatalf("stream: %v", err)
		}
		parts = append(parts, p)
	}
	return parts
}

func baseRequest() Request {
	return Request{
		Model:        "claude-sonnet-5-5",
		System:       []string{"rules", "facts"},
		Tools:        []ToolDef{{Name: "alerts", Description: "a", Schema: json.RawMessage(`{"type":"object","properties":{"range":{"type":"string"}}}`)}, {Name: "cite", Description: "c", Schema: json.RawMessage(`{"type":"object","properties":{},"required":[]}`)}},
		Messages:     []Message{{Role: "user", Parts: []Part{txt("hi")}}},
		MaxTokens:    777,
		Effort:       "low",
		Conversation: "conv1",
	}
}

func TestAnthropicRequest(t *testing.T) {
	srv := newAnthropicServer(t, "testdata/anthropic_stream.txt")
	cfg := config.AssistantConfig{BaseURL: srv.URL, Headers: map[string]string{"X-Test": "1"}}
	m := NewAnthropicModel(cfg, "test-key", srv.Client())
	collect(t, m, baseRequest())
	body := srv.last()

	if got := names2(parse(body).get("tools")); got != "alerts,cite" {
		t.Fatalf("tool names %s", got)
	}
	if parse(body).get("tools", 0, "cache_control").exists() || parse(body).get("tools", 1, "cache_control", "type").str() != "ephemeral" {
		t.Fatalf("tool cache_control: %s", parse(body).get("tools").raw())
	}
	if parse(body).get("system", 0, "cache_control").exists() || parse(body).get("system", 1, "cache_control", "type").str() != "ephemeral" {
		t.Fatalf("system cache_control: %s", parse(body).get("system").raw())
	}
	if got := parse(body).get("tools").find("name", "alerts").get("eager_input_streaming"); !got.on() {
		t.Fatalf("eager_input_streaming: %s", body)
	}
	if parse(body).get("max_tokens").num() != 777 || parse(body).get("tool_choice", "type").str() != "auto" || parse(body).get("output_config", "effort").str() != "low" {
		t.Fatalf("body %s", body)
	}
	if !parse(body).get("stream").on() || parse(body).get("thinking").exists() {
		t.Fatalf("stream or thinking: %s", body)
	}
	if parse(body).get("fallbacks").exists() {
		t.Fatal("fallbacks sent to a custom base_url")
	}
	if got := parse(body).get("metadata", "user_id").str(); got != "conv1" {
		t.Fatalf("metadata.user_id %q", got)
	}
	h := srv.headers[0]
	if h.Get("X-Api-Key") != "test-key" || h.Get("X-Test") != "1" {
		t.Fatalf("headers %v", h)
	}
}

func TestAnthropicNoToolsAndWebSearch(t *testing.T) {
	srv := newAnthropicServer(t, "testdata/anthropic_stream.txt")
	m := NewAnthropicModel(config.AssistantConfig{BaseURL: srv.URL}, "k", srv.Client())

	r := baseRequest()
	collect(t, m, r)
	if strings.Contains(srv.last(), "web_search") {
		t.Fatal("web search tool sent without the plan flag")
	}

	r.WebSearch, r.NoTools = true, true
	collect(t, m, r)
	body := srv.last()
	ws := parse(body).get("tools").find("type", "web_search_20260209")
	if !ws.exists() || ws.get("max_uses").num() != 3 || ws.get("cache_control", "type").str() != "ephemeral" {
		t.Fatalf("web search tool: %s", parse(body).get("tools").raw())
	}
	if parse(body).get("tools", 1, "cache_control").exists() {
		t.Fatal("cache_control left on the client tool")
	}
	if parse(body).get("tool_choice", "type").str() != "none" {
		t.Fatalf("tool_choice %s", parse(body).get("tool_choice").raw())
	}

	// The model does not check the support. The limits do.
	r.Model, r.NoTools = "deepseek-v4.1-flash", false
	collect(t, m, r)
	if !parse(srv.last()).get("tools").find("type", "web_search_20260209").exists() {
		t.Fatal("web search tool not sent for WebSearch")
	}
}

func TestWebSearchSupported(t *testing.T) {
	for _, tc := range []struct {
		name  string
		cfg   config.AssistantConfig
		model string
		want  bool
	}{
		{"auto claude", config.AssistantConfig{}, "claude-sonnet-5-5", true},
		{"auto haiku", config.AssistantConfig{}, "claude-haiku-4-5", false},
		{"auto deepseek", config.AssistantConfig{}, "deepseek-v4.1-flash", false},
		{"auto claude openai", config.AssistantConfig{APIFormat: "openai"}, "claude-sonnet-5-5", false},
		{"always deepseek openai", config.AssistantConfig{APIFormat: "openai", WebSearchSupport: config.WebSearchAlways}, "deepseek-v4.1-flash", true},
		{"never claude", config.AssistantConfig{WebSearchSupport: config.WebSearchNever}, "claude-sonnet-5-5", false},
	} {
		if got := webSearchSupported(tc.cfg, tc.model); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestAnthropicSearchParts(t *testing.T) {
	srv := newAnthropicServer(t, "testdata/anthropic_search_stream.txt")
	m := NewAnthropicModel(config.AssistantConfig{BaseURL: srv.URL}, "k", srv.Client())
	parts := collect(t, m, baseRequest())

	var searches []SearchCall
	var asst []Part
	var kinds []string
	for _, p := range parts {
		kinds = append(kinds, string(p.Kind))
		switch p.Kind {
		case PartSearch:
			searches = append(searches, *p.Search)
		case PartNative, PartText:
			asst = append(asst, p)
		}
	}
	want := []SearchCall{{ID: "srvtoolu_01", Query: "towertail release"}, {ID: "srvtoolu_01", Results: 2}}
	if !slices.Equal(searches, want) {
		t.Fatalf("searches %+v, want %+v", searches, want)
	}
	// Each search part comes before the native block of it.
	if got := strings.Join(kinds, ","); got != "text,native,search,native,search,native,text,native,stop" {
		t.Fatalf("parts %s", got)
	}
	last := parts[len(parts)-1]
	if last.Stop != StopEnd || last.Usage.WebSearches != 1 || last.Usage.Output != 30 {
		t.Fatalf("last %+v %+v", last, *last.Usage)
	}
	natives := slices.DeleteFunc(slices.Clone(asst), func(p Part) bool { return p.Kind != PartNative })
	if parse(string(natives[1].Native)).get("input", "query").str() != "towertail release" {
		t.Fatalf("server_tool_use block %s", natives[1].Native)
	}
	if res := parse(string(natives[2].Native)); res.get("tool_use_id").str() != "srvtoolu_01" || len(res.get("content").arr()) != 2 {
		t.Fatalf("web_search_tool_result block %s", natives[2].Native)
	}

	r := baseRequest()
	r.Messages = append(r.Messages, Message{Role: "assistant", Parts: asst}, Message{Role: "user", Parts: []Part{txt("more")}})
	collect(t, m, r)
	blocks := parse(srv.last()).get("messages", 1, "content").arr()
	if len(blocks) != len(natives) {
		t.Fatalf("assistant blocks %d: %s", len(blocks), parse(srv.last()).get("messages", 1).raw())
	}
	for i, p := range natives {
		if !jsonEqual(json.RawMessage(blocks[i].raw()), string(p.Native)) {
			t.Fatalf("block %d not replayed unchanged: %s, want %s", i, blocks[i].raw(), p.Native)
		}
	}
}

func TestAnthropicSearchError(t *testing.T) {
	blk := `{"type":"web_search_tool_result","tool_use_id":"srvtoolu_02","content":{"type":"web_search_tool_result_error","error_code":"max_uses_exceeded"}}`
	var u anthropic.ContentBlockUnion
	if err := u.UnmarshalJSON([]byte(blk)); err != nil {
		t.Fatal(err)
	}
	if got := anthropicSearch(u); got == nil || *got != (SearchCall{ID: "srvtoolu_02", Err: "max_uses_exceeded"}) {
		t.Fatalf("search %+v", got)
	}
	_ = u.UnmarshalJSON([]byte(`{"type":"server_tool_use","id":"srvtoolu_03","name":"web_fetch","input":{"url":"https://x"}}`))
	if got := anthropicSearch(u); got != nil {
		t.Fatalf("web_fetch gave a search %+v", got)
	}
}

func TestAnthropicStreamParts(t *testing.T) {
	srv := newAnthropicServer(t, "testdata/anthropic_stream.txt")
	m := NewAnthropicModel(config.AssistantConfig{BaseURL: srv.URL}, "k", srv.Client())
	parts := collect(t, m, baseRequest())

	var text string
	var calls []*ToolCall
	var natives []Part
	for _, p := range parts {
		switch p.Kind {
		case PartText:
			text += p.Text
		case PartToolCall:
			calls = append(calls, p.Call)
		case PartNative:
			natives = append(natives, p)
		}
	}
	if text != "Let me check." {
		t.Fatalf("text %q", text)
	}
	if len(calls) != 1 || calls[0].ID != "toolu_01" || calls[0].Name != "alerts" || calls[0].Err != "" {
		t.Fatalf("calls %+v", calls)
	}
	if string(calls[0].Input) != `{"range": "30d", "limit": 5}` && !jsonEqual(calls[0].Input, `{"range":"30d","limit":5}`) {
		t.Fatalf("input %s", calls[0].Input)
	}
	last := parts[len(parts)-1]
	if last.Kind != PartStop || last.Stop != StopToolUse {
		t.Fatalf("last %+v", last)
	}
	if want := (Usage{Input: 20, Output: 42, CacheRead: 100, CacheWrite: 30}); *last.Usage != want {
		t.Fatalf("usage %+v, want %+v", *last.Usage, want)
	}
	if len(natives) != 3 {
		t.Fatalf("natives %d", len(natives))
	}
	if th := natives[0].Native; parse(string(th)).get("type").str() != "thinking" || parse(string(th)).get("signature").str() != "sig-abc" || parse(string(th)).get("thinking").str() != "I should read the alerts." {
		t.Fatalf("thinking block %s", th)
	}
	if parse(string(natives[1].Native)).get("text").str() != "Let me check." {
		t.Fatalf("text block %s", natives[1].Native)
	}
	if tu := natives[2].Native; parse(string(tu)).get("id").str() != "toolu_01" || parse(string(tu)).get("input", "range").str() != "30d" {
		t.Fatalf("tool_use block %s", tu)
	}
}

func jsonEqual(raw json.RawMessage, want string) bool {
	var a, b any
	return json.Unmarshal(raw, &a) == nil && json.Unmarshal([]byte(want), &b) == nil && func() bool {
		x, _ := json.Marshal(a)
		y, _ := json.Marshal(b)
		return string(x) == string(y)
	}()
}

func TestAnthropicNativeReplay(t *testing.T) {
	srv := newAnthropicServer(t, "testdata/anthropic_stream.txt")
	m := NewAnthropicModel(config.AssistantConfig{BaseURL: srv.URL}, "k", srv.Client())
	parts := collect(t, m, baseRequest())
	var asst []Part
	var call *ToolCall
	for _, p := range parts {
		switch p.Kind {
		case PartNative:
			asst = append(asst, p)
		case PartToolCall:
			call = p.Call
			asst = append(asst, p)
		case PartText:
			asst = append(asst, p)
		}
	}
	r := baseRequest()
	r.Messages = append(r.Messages,
		Message{Role: "assistant", Parts: asst},
		Message{Role: "user", Parts: []Part{{Kind: PartToolResult, Result: &ToolResult{CallID: call.ID, Content: json.RawMessage(`{"n":1}`)}}}},
	)
	collect(t, m, r)
	body := srv.last()
	blocks := parse(body).get("messages", 1, "content").arr()
	if len(blocks) != 3 {
		t.Fatalf("assistant blocks %d: %s", len(blocks), parse(body).get("messages", 1).raw())
	}
	for i, p := range parts {
		_ = i
		if p.Kind != PartNative {
			continue
		}
		if !slices.ContainsFunc(blocks, func(b jv) bool { return jsonEqual(json.RawMessage(b.raw()), string(p.Native)) }) {
			t.Fatalf("block not replayed unchanged: %s\nbody: %s", p.Native, parse(body).get("messages", 1).raw())
		}
	}
	res := parse(body).get("messages", 2, "content", 0)
	if res.get("type").str() != "tool_result" || res.get("tool_use_id").str() != "toolu_01" {
		t.Fatalf("tool result %s", res.raw())
	}
	if len(parse(body).get("messages").arr()) != 3 {
		t.Fatalf("messages %s", parse(body).get("messages").raw())
	}
}

func TestAnthropicFallbacks(t *testing.T) {
	cases := []struct {
		name  string
		base  string
		model string
		want  bool
	}{
		{"default url", "", fallbackModel, true},
		{"explicit default url", "https://api.anthropic.com/", fallbackModel, true},
		{"custom url", "https://proxy.example/anthropic", fallbackModel, false},
		{"other model", "", "claude-opus-5-5", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := NewAnthropicModel(config.AssistantConfig{BaseURL: tc.base}, "k", nil)
			opts := m.requestOptions(Request{Model: tc.model})
			if (len(opts) == 2) != tc.want || (len(opts) != 0 && len(opts) != 2) {
				t.Fatalf("got %d options", len(opts))
			}
		})
	}
}

func TestAnthropicFallbacksOnTheWire(t *testing.T) {
	// The default base URL cannot be a test server. Send the same options
	// through a transport that answers for api.anthropic.com.
	stream, err := os.ReadFile("testdata/anthropic_stream.txt")
	if err != nil {
		t.Fatal(err)
	}
	var body string
	var beta string
	hc := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		b, _ := io.ReadAll(r.Body)
		body, beta = string(b), r.Header.Get("anthropic-beta")
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(string(stream))), Request: r}, nil
	})}
	m := NewAnthropicModel(config.AssistantConfig{}, "k", hc)
	collect(t, m, baseRequest())
	if parse(body).get("fallbacks").str() != "default" || !strings.Contains(beta, "server-side-fallback-2026-07-01") {
		t.Fatalf("fallbacks %q beta %q", parse(body).get("fallbacks").raw(), beta)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestAnthropicProviderError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(400)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"invalid_request_error","message":"bad"}}`))
	}))
	defer srv.Close()
	m := NewAnthropicModel(config.AssistantConfig{BaseURL: srv.URL}, "k", srv.Client())
	var got error
	for _, err := range m.Stream(t.Context(), baseRequest()) {
		got = err
	}
	var pe *ProviderError
	if !asProviderError(got, &pe) || pe.Status != 400 {
		t.Fatalf("err %v", got)
	}
}

func asProviderError(err error, target **ProviderError) bool {
	pe, ok := err.(*ProviderError)
	if ok {
		*target = pe
	}
	return ok
}

func names2(tools jv) string {
	var out []string
	for _, t := range tools.arr() {
		out = append(out, t.get("name").str())
	}
	return strings.Join(out, ",")
}
