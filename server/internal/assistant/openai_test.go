package assistant

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/towertail/towertail/server/config"
)

const openaiStream = `data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":"Let me "}}]}

data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"check."}}]}

data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_a","type":"function","function":{"name":"alerts","arguments":""}}]}}]}

data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"id":"call_b","type":"function","function":{"name":"host_metrics","arguments":"{\"host\":"}}]}}]}

data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"range\":"}}]}}]}

data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"function":{"arguments":"\"web-1\"}"}}]}}]}

data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"30d\"}"}}]}}]}

data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}

data: {"id":"c1","object":"chat.completion.chunk","choices":[],"usage":{"prompt_tokens":150,"completion_tokens":30,"total_tokens":180,"prompt_tokens_details":{"cached_tokens":100}}}

data: [DONE]

`

func TestOpenAIStream(t *testing.T) {
	var body, path, auth, custom string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		body, path, auth, custom = string(b), r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("X-Test")
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(openaiStream))
	}))
	defer srv.Close()

	cfg := config.AssistantConfig{APIFormat: "openai", BaseURL: srv.URL + "/v1", Headers: map[string]string{"X-Test": "1"}}
	m := NewOpenAIModel(cfg, "test-key", srv.Client())
	r := baseRequest()
	r.Model = "gpt-test"
	parts := collect(t, m, r)

	if path != "/v1/chat/completions" || auth != "Bearer test-key" || custom != "1" {
		t.Fatalf("path %q auth %q header %q", path, auth, custom)
	}
	b := parse(body)
	if !b.get("stream").on() || !b.get("stream_options", "include_usage").on() || b.get("max_completion_tokens").num() != 777 {
		t.Fatalf("body %s", body)
	}
	if b.get("tool_choice").exists() {
		t.Fatalf("tool_choice sent: %s", b.get("tool_choice").raw())
	}
	if strings.Contains(body, "cache_control") || strings.Contains(body, "web_search") {
		t.Fatalf("anthropic-only field in body: %s", body)
	}
	if got := b.get("messages", 0, "role").str(); got != "system" {
		t.Fatalf("first role %q", got)
	}
	if b.get("tools", 0, "function", "name").str() != "alerts" {
		t.Fatalf("tools %s", b.get("tools").raw())
	}

	var text string
	var calls []*ToolCall
	for _, p := range parts {
		switch p.Kind {
		case PartText:
			text += p.Text
		case PartToolCall:
			calls = append(calls, p.Call)
		case PartNative:
			t.Fatal("openai gave a native part")
		}
	}
	if text != "Let me check." || len(calls) != 2 {
		t.Fatalf("text %q calls %d", text, len(calls))
	}
	if calls[0].ID != "call_a" || calls[0].Name != "alerts" || string(calls[0].Input) != `{"range":"30d"}` {
		t.Fatalf("call 0 %+v %s", calls[0], calls[0].Input)
	}
	if calls[1].ID != "call_b" || calls[1].Name != "host_metrics" || string(calls[1].Input) != `{"host":"web-1"}` {
		t.Fatalf("call 1 %+v %s", calls[1], calls[1].Input)
	}
	last := parts[len(parts)-1]
	if last.Kind != PartStop || last.Stop != StopToolUse {
		t.Fatalf("last %+v", last)
	}
	if want := (Usage{Input: 50, Output: 30, CacheRead: 100}); *last.Usage != want {
		t.Fatalf("usage %+v, want %+v", *last.Usage, want)
	}
}

func TestOpenAIToolsAndHistory(t *testing.T) {
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"id\":\"c\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"))
	}))
	defer srv.Close()
	m := NewOpenAIModel(config.AssistantConfig{BaseURL: srv.URL}, "k", srv.Client())
	r := baseRequest()
	r.NoTools = true
	r.Messages = append(r.Messages,
		Message{Role: "assistant", Parts: []Part{txt("looking"), call("call_a", "alerts", `{}`)}},
		Message{Role: "user", Parts: []Part{{Kind: PartToolResult, Result: &ToolResult{CallID: "call_a", Content: []byte(`{"n":1}`)}}}},
	)
	parts := collect(t, m, r)
	b := parse(body)
	if b.get("user").str() != "conv1" {
		t.Fatalf("user %s", b.get("user").raw())
	}
	if b.get("tool_choice").str() != "none" {
		t.Fatalf("tool_choice %s", b.get("tool_choice").raw())
	}
	asst := b.get("messages", 2)
	if asst.get("role").str() != "assistant" || asst.get("tool_calls", 0, "id").str() != "call_a" || asst.get("content").str() != "looking" {
		t.Fatalf("assistant message %s", asst.raw())
	}
	tool := b.get("messages", 3)
	if tool.get("role").str() != "tool" || tool.get("tool_call_id").str() != "call_a" {
		t.Fatalf("tool message %s", tool.raw())
	}
	if last := parts[len(parts)-1]; last.Stop != StopEnd {
		t.Fatalf("stop %+v", last)
	}
}

func TestNewModelPicksProvider(t *testing.T) {
	get := func(k string) string {
		if k == "ANTHROPIC_API_KEY" || k == "OPENAI_API_KEY" {
			return "k"
		}
		return ""
	}
	m, err := NewModel(config.AssistantConfig{Model: "m"}, get, nil)
	if _, ok := m.(*AnthropicModel); err != nil || !ok {
		t.Fatalf("anthropic: %T %v", m, err)
	}
	m, err = NewModel(config.AssistantConfig{Model: "m", APIFormat: "openai"}, get, nil)
	if _, ok := m.(*OpenAIModel); err != nil || !ok {
		t.Fatalf("openai: %T %v", m, err)
	}
	if _, err := NewModel(config.AssistantConfig{Model: "m"}, func(string) string { return "" }, nil); err == nil {
		t.Fatal("no key accepted")
	}
}

// openaiServer answers with stream and keeps the last request body.
func openaiServer(t *testing.T, stream string, body *string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		*body = string(b)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(stream))
	}))
	t.Cleanup(srv.Close)
	return srv
}

const openaiShortStream = "data: {\"id\":\"c\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"

func TestOpenAIWebSearchOptions(t *testing.T) {
	var body string
	srv := openaiServer(t, openaiShortStream, &body)
	m := NewOpenAIModel(config.AssistantConfig{BaseURL: srv.URL}, "k", srv.Client())
	r := baseRequest()
	r.WebSearch = true
	collect(t, m, r)
	ws := parse(body).get("web_search_options")
	if ws.get("search_context_size").str() != "low" || ws.get("max_uses").num() != webSearchUses {
		t.Fatalf("web_search_options %s", ws.raw())
	}
	if !parse(body).get("stream").on() || parse(body).get("tools", 0, "function", "name").str() != "alerts" {
		t.Fatalf("body %s", body)
	}

	// OpenAI itself does not get the field max_uses.
	hc := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		b, _ := io.ReadAll(req.Body)
		body = string(b)
		if req.URL.Host != "api.openai.com" {
			t.Errorf("host %s", req.URL.Host)
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(openaiShortStream)), Request: req}, nil
	})}
	collect(t, NewOpenAIModel(config.AssistantConfig{}, "k", hc), r)
	ws = parse(body).get("web_search_options")
	if ws.get("search_context_size").str() != "low" || ws.get("max_uses").exists() {
		t.Fatalf("web_search_options to OpenAI %s", ws.raw())
	}
}

func TestOpenAISearchUsageAndAnnotations(t *testing.T) {
	stream := `data: {"id":"c","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":"See A and B."}}]}

data: {"id":"c","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"annotations":[{"type":"url_citation","url_citation":{"url":"https://a.example","title":"A","start_index":4,"end_index":5}},{"type":"url_citation","url_citation":{"url":"https://b.example","title":"B","start_index":10,"end_index":11}},{"type":"url_citation","url_citation":{"url":"https://a.example","title":"A","start_index":0,"end_index":3}}]},"finish_reason":"stop"}]}

data: {"id":"c","object":"chat.completion.chunk","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15,"web_search_requests":2}}

data: [DONE]

`
	var body string
	srv := openaiServer(t, stream, &body)
	m := NewOpenAIModel(config.AssistantConfig{BaseURL: srv.URL}, "k", srv.Client())
	r := baseRequest()
	r.WebSearch = true
	parts := collect(t, m, r)
	if len(parts) < 2 {
		t.Fatalf("parts %+v", parts)
	}
	s, last := parts[len(parts)-2], parts[len(parts)-1]
	if s.Kind != PartSearch || *s.Search != (SearchCall{ID: "oai_search", Results: 2}) {
		t.Fatalf("search part %+v", s)
	}
	if last.Kind != PartStop || last.Usage.WebSearches != 2 || last.Usage.Input != 10 {
		t.Fatalf("last %+v", last)
	}
}

func TestOpenAIStreamError(t *testing.T) {
	stream := `data: {"id":"c","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"Par"}}]}

data: {"error":{"message":"The model failed.","type":"server_error","code":"run_failed"}}

`
	var body string
	srv := openaiServer(t, stream, &body)
	m := NewOpenAIModel(config.AssistantConfig{BaseURL: srv.URL}, "k", srv.Client())
	var got error
	var stops int
	for p, err := range m.Stream(t.Context(), baseRequest()) {
		if err != nil {
			got = err
		}
		if p.Kind == PartStop {
			stops++
		}
	}
	var pe *ProviderError
	if !asProviderError(got, &pe) || !strings.Contains(got.Error(), "The model failed.") || stops != 0 {
		t.Fatalf("err %v, stops %d", got, stops)
	}
}
