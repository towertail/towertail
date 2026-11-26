package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"iter"
	"maps"
	"net/http"
	"slices"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/anthropics/anthropic-sdk-go/packages/param"

	"github.com/towertail/towertail/server/config"
)

const (
	// fallbackModel is the model that gets server-side refusal fallbacks.
	fallbackModel   = "claude-sonnet-5-5"
	webSearchUses   = 3
	defaultAnthBase = "https://api.anthropic.com"
)

// webSearchModels are the model id prefixes that support web_search_20260209.
var webSearchModels = []string{
	"claude-fable-", "claude-mythos-", "claude-opus-5", "claude-opus-4-8",
	"claude-opus-4-7", "claude-opus-4-6", "claude-sonnet-5", "claude-sonnet-4-6",
}

// AnthropicModel streams from the Anthropic Messages API.
type AnthropicModel struct {
	client anthropic.Client
	cfg    config.AssistantConfig
}

// NewAnthropicModel makes the model. The client does not read the
// environment, so only cfg and key set the connection.
func NewAnthropicModel(cfg config.AssistantConfig, key string, hc *http.Client) *AnthropicModel {
	opts := []option.RequestOption{option.WithoutEnvironmentDefaults(), option.WithAPIKey(key)}
	if cfg.BaseURL != "" {
		opts = append(opts, option.WithBaseURL(cfg.BaseURL))
	}
	if hc != nil {
		opts = append(opts, option.WithHTTPClient(hc))
	}
	for _, k := range slices.Sorted(maps.Keys(cfg.Headers)) {
		opts = append(opts, option.WithHeader(k, cfg.Headers[k]))
	}
	return &AnthropicModel{client: anthropic.NewClient(opts...), cfg: cfg}
}

// Stream sends r and gives the parts of the answer. A text delta gives a
// PartText. A finished block gives a PartToolCall (for a tool call) or a
// PartSearch (for a web search block), and a PartNative. The last part is
// a PartStop with the usage.
func (m *AnthropicModel) Stream(ctx context.Context, r Request) iter.Seq2[Part, error] {
	return func(yield func(Part, error) bool) {
		params, err := m.params(r)
		if err != nil {
			yield(Part{}, err)
			return
		}
		stream := m.client.Messages.NewStreaming(ctx, params, m.requestOptions(r)...)
		defer stream.Close()

		var msg anthropic.Message
		inputs := map[int64]*strings.Builder{}
		for stream.Next() {
			ev := stream.Current()
			if err := msg.Accumulate(ev); err != nil {
				yield(Part{}, &ProviderError{Err: err})
				return
			}
			switch ev.Type {
			case "content_block_delta":
				switch ev.Delta.Type {
				case "text_delta":
					if !yield(Part{Kind: PartText, Text: ev.Delta.Text}, nil) {
						return
					}
				case "input_json_delta":
					b := inputs[ev.Index]
					if b == nil {
						b = &strings.Builder{}
						inputs[ev.Index] = b
					}
					b.WriteString(ev.Delta.PartialJSON)
				}
			case "content_block_stop":
				blk := msg.Content[ev.Index]
				if blk.Type == "tool_use" {
					if !yield(Part{Kind: PartToolCall, Call: anthropicCall(blk, inputs[ev.Index])}, nil) {
						return
					}
				}
				if s := anthropicSearch(blk); s != nil {
					if !yield(Part{Kind: PartSearch, Search: s}, nil) {
						return
					}
				}
				if !yield(Part{Kind: PartNative, Native: json.RawMessage(blk.RawJSON())}, nil) {
					return
				}
			}
		}
		if err := stream.Err(); err != nil {
			yield(Part{}, providerError(err))
			return
		}
		yield(Part{Kind: PartStop, Stop: anthropicStop(msg.StopReason), Usage: &Usage{
			Input:       int(msg.Usage.InputTokens),
			Output:      int(msg.Usage.OutputTokens),
			CacheRead:   int(msg.Usage.CacheReadInputTokens),
			CacheWrite:  int(msg.Usage.CacheCreationInputTokens),
			WebSearches: int(msg.Usage.ServerToolUse.WebSearchRequests),
		}}, nil)
	}
}

// anthropicCall reads a finished tool_use block. A partial input that is
// not valid JSON sets Err.
func anthropicCall(blk anthropic.ContentBlockUnion, partial *strings.Builder) *ToolCall {
	c := &ToolCall{ID: blk.ID, Name: blk.Name, Input: json.RawMessage(blk.Input)}
	if partial != nil && !json.Valid([]byte(partial.String())) {
		c.Input = nil
		c.Err = "The tool input is not valid JSON. The output may have been cut off."
	}
	return c
}

// anthropicSearch reads a finished web search block: the server_tool_use
// gives the query, the web_search_tool_result gives the result count or the
// error code. Other blocks give nil.
func anthropicSearch(blk anthropic.ContentBlockUnion) *SearchCall {
	switch blk.Type {
	case "server_tool_use":
		if blk.Name != "web_search" {
			return nil
		}
		var in struct {
			Query string `json:"query"`
		}
		_ = json.Unmarshal(blk.Input, &in)
		return &SearchCall{ID: blk.ID, Query: in.Query}
	case "web_search_tool_result":
		var res struct {
			ToolUseID string          `json:"tool_use_id"`
			Content   json.RawMessage `json:"content"`
		}
		if json.Unmarshal([]byte(blk.RawJSON()), &res) != nil {
			return nil
		}
		s := &SearchCall{ID: res.ToolUseID}
		var items []json.RawMessage
		var fail struct {
			ErrorCode string `json:"error_code"`
		}
		switch {
		case json.Unmarshal(res.Content, &items) == nil:
			s.Results = len(items)
		case json.Unmarshal(res.Content, &fail) == nil && fail.ErrorCode != "":
			s.Err = fail.ErrorCode
		default:
			s.Err = "unknown"
		}
		return s
	}
	return nil
}

func anthropicStop(s anthropic.StopReason) StopReason {
	switch s {
	case anthropic.StopReasonToolUse:
		return StopToolUse
	case anthropic.StopReasonMaxTokens, anthropic.StopReasonModelContextWindowExceeded:
		return StopMaxTokens
	case anthropic.StopReasonRefusal:
		return StopRefusal
	case anthropic.StopReasonPauseTurn:
		return StopPause
	}
	return StopEnd
}

func providerError(err error) error {
	var ae *anthropic.Error
	if errors.As(err, &ae) {
		return &ProviderError{Status: ae.StatusCode, Err: err}
	}
	return err
}

// requestOptions adds the refusal fallback for the model that supports it
// on the first-party API.
func (m *AnthropicModel) requestOptions(r Request) []option.RequestOption {
	base := strings.TrimRight(m.cfg.BaseURL, "/")
	if r.Model != fallbackModel || (base != "" && base != defaultAnthBase) {
		return nil
	}
	return []option.RequestOption{
		option.WithJSONSet("fallbacks", "default"),
		option.WithHeaderAdd("anthropic-beta", string(anthropic.AnthropicBetaServerSideFallback2026_07_01)),
	}
}

func (m *AnthropicModel) params(r Request) (anthropic.MessageNewParams, error) {
	p := anthropic.MessageNewParams{
		Model:     r.Model,
		MaxTokens: int64(r.MaxTokens),
		ToolChoice: anthropic.ToolChoiceUnionParam{
			OfAuto: &anthropic.ToolChoiceAutoParam{},
		},
	}
	if r.Conversation != "" {
		p.Metadata = anthropic.MetadataParam{UserID: param.NewOpt(r.Conversation)}
	}
	if r.NoTools {
		none := anthropic.NewToolChoiceNoneParam()
		p.ToolChoice = anthropic.ToolChoiceUnionParam{OfNone: &none}
	}
	if r.Effort != "" {
		p.OutputConfig = anthropic.OutputConfigParam{Effort: anthropic.OutputConfigEffort(r.Effort)}
	}
	for _, s := range r.System {
		p.System = append(p.System, anthropic.TextBlockParam{Text: s})
	}
	if n := len(p.System); n > 0 {
		p.System[n-1].CacheControl = anthropic.NewCacheControlEphemeralParam()
	}
	for _, d := range r.Tools {
		t, err := anthropicTool(d)
		if err != nil {
			return p, err
		}
		p.Tools = append(p.Tools, t)
	}
	if r.WebSearch {
		p.Tools = append(p.Tools, anthropic.ToolUnionParam{OfWebSearchTool20260209: &anthropic.WebSearchTool20260209Param{
			MaxUses: param.NewOpt(int64(webSearchUses)),
		}})
	}
	if n := len(p.Tools); n > 0 {
		last := &p.Tools[n-1]
		switch {
		case last.OfTool != nil:
			last.OfTool.CacheControl = anthropic.NewCacheControlEphemeralParam()
		case last.OfWebSearchTool20260209 != nil:
			last.OfWebSearchTool20260209.CacheControl = anthropic.NewCacheControlEphemeralParam()
		}
	}
	for _, msg := range r.Messages {
		p.Messages = append(p.Messages, anthropicMessage(msg))
	}
	return p, nil
}

// webSearchSupported tells if model can get the web search tool. Auto
// allows only a Claude model with the anthropic format.
func webSearchSupported(cfg config.AssistantConfig, model string) bool {
	switch cfg.WebSearchSupport {
	case config.WebSearchNever:
		return false
	case config.WebSearchAlways:
		return true
	}
	return cfg.Format() == "anthropic" &&
		slices.ContainsFunc(webSearchModels, func(p string) bool { return strings.HasPrefix(model, p) })
}

func anthropicTool(d ToolDef) (anthropic.ToolUnionParam, error) {
	var schema map[string]any
	if err := json.Unmarshal(d.Schema, &schema); err != nil {
		return anthropic.ToolUnionParam{}, errors.New("assistant: tool " + d.Name + ": bad schema: " + err.Error())
	}
	in := anthropic.ToolInputSchemaParam{Properties: schema["properties"]}
	if req, ok := schema["required"].([]any); ok {
		for _, v := range req {
			if s, ok := v.(string); ok {
				in.Required = append(in.Required, s)
			}
		}
	}
	extra := map[string]any{}
	for k, v := range schema {
		if k != "properties" && k != "required" && k != "type" {
			extra[k] = v
		}
	}
	if len(extra) > 0 {
		in.ExtraFields = extra
	}
	return anthropic.ToolUnionParam{OfTool: &anthropic.ToolParam{
		Name:                d.Name,
		Description:         anthropic.String(d.Description),
		InputSchema:         in,
		EagerInputStreaming: anthropic.Bool(true),
	}}, nil
}

// anthropicMessage converts a message. An assistant message with native
// blocks goes back as those blocks, unchanged.
func anthropicMessage(m Message) anthropic.MessageParam {
	var blocks []anthropic.ContentBlockParamUnion
	native := m.Role == "assistant" && slices.ContainsFunc(m.Parts, func(p Part) bool { return p.Kind == PartNative })
	for _, p := range m.Parts {
		switch {
		case native && p.Kind == PartNative:
			blocks = append(blocks, param.Override[anthropic.ContentBlockParamUnion](p.Native))
		case native:
		case p.Kind == PartText && p.Text != "":
			blocks = append(blocks, anthropic.NewTextBlock(p.Text))
		case p.Kind == PartToolCall:
			input := p.Call.Input
			if len(input) == 0 {
				input = json.RawMessage(`{}`)
			}
			blocks = append(blocks, anthropic.NewToolUseBlock(p.Call.ID, input, p.Call.Name))
		case p.Kind == PartToolResult:
			blocks = append(blocks, anthropic.NewToolResultBlock(p.Result.CallID, string(p.Result.Content), p.Result.IsError))
		}
	}
	if m.Role == "assistant" {
		return anthropic.NewAssistantMessage(blocks...)
	}
	return anthropic.NewUserMessage(blocks...)
}
