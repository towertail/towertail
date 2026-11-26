package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"iter"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/packages/param"
	"github.com/openai/openai-go/v3/packages/ssestream"
	"github.com/openai/openai-go/v3/shared"

	"github.com/towertail/towertail/server/config"
)

const defaultOpenAIBase = "https://api.openai.com/v1"

// OpenAIModel streams from the Chat Completions API of OpenAI or of a
// compatible server. It sends no cache markers. For WebSearch it sends
// web_search_options.
type OpenAIModel struct {
	client openai.Client
	cfg    config.AssistantConfig
}

// NewOpenAIModel makes the model. cfg.BaseURL sets the server.
func NewOpenAIModel(cfg config.AssistantConfig, key string, hc *http.Client) *OpenAIModel {
	base := cfg.BaseURL
	if base == "" {
		base = defaultOpenAIBase
	}
	opts := []option.RequestOption{option.WithAPIKey(key), option.WithBaseURL(base)}
	if hc != nil {
		opts = append(opts, option.WithHTTPClient(hc))
	}
	for _, k := range slices.Sorted(maps.Keys(cfg.Headers)) {
		opts = append(opts, option.WithHeader(k, cfg.Headers[k]))
	}
	return &OpenAIModel{client: openai.NewClient(opts...), cfg: cfg}
}

// pendingCall joins the fragments of one tool call.
type pendingCall struct {
	id, name string
	args     strings.Builder
}

// Stream sends r and gives the parts of the answer, in the same form as
// AnthropicModel.Stream, but with no PartNative.
func (m *OpenAIModel) Stream(ctx context.Context, r Request) iter.Seq2[Part, error] {
	return func(yield func(Part, error) bool) {
		params, err := m.params(r)
		if err != nil {
			yield(Part{}, err)
			return
		}
		stream := m.client.Chat.Completions.NewStreaming(ctx, params, m.requestOptions(r)...)
		defer stream.Close()

		calls := map[int64]*pendingCall{}
		cited := map[string]bool{}
		var usage Usage
		var finish string
		var refused bool
		for stream.Next() {
			chunk := stream.Current()
			if u := chunk.Usage; u.JSON.TotalTokens.Valid() {
				cached := int(u.PromptTokensDetails.CachedTokens)
				usage = Usage{
					Input:     max(int(u.PromptTokens)-cached, 0),
					Output:    int(u.CompletionTokens),
					CacheRead: cached,
				}
				if f, ok := u.JSON.ExtraFields["web_search_requests"]; ok {
					_ = json.Unmarshal([]byte(f.Raw()), &usage.WebSearches)
				}
			}
			for _, c := range chunk.Choices {
				if f, ok := c.Delta.JSON.ExtraFields["annotations"]; ok {
					var anns []struct {
						Type        string `json:"type"`
						URLCitation struct {
							URL string `json:"url"`
						} `json:"url_citation"`
					}
					_ = json.Unmarshal([]byte(f.Raw()), &anns)
					for _, a := range anns {
						if a.Type == "url_citation" && a.URLCitation.URL != "" {
							cited[a.URLCitation.URL] = true
						}
					}
				}
				if c.Delta.Content != "" {
					if !yield(Part{Kind: PartText, Text: c.Delta.Content}, nil) {
						return
					}
				}
				if c.Delta.Refusal != "" {
					refused = true
				}
				for _, tc := range c.Delta.ToolCalls {
					pc := calls[tc.Index]
					if pc == nil {
						pc = &pendingCall{}
						calls[tc.Index] = pc
					}
					if tc.ID != "" {
						pc.id = tc.ID
					}
					if tc.Function.Name != "" {
						pc.name = tc.Function.Name
					}
					pc.args.WriteString(tc.Function.Arguments)
				}
				if c.FinishReason != "" {
					finish = c.FinishReason
				}
			}
		}
		if err := stream.Err(); err != nil {
			yield(Part{}, openaiError(err))
			return
		}
		for _, i := range slices.Sorted(maps.Keys(calls)) {
			if !yield(Part{Kind: PartToolCall, Call: pc2call(calls[i])}, nil) {
				return
			}
		}
		if usage.WebSearches > 0 || len(cited) > 0 {
			if !yield(Part{Kind: PartSearch, Search: &SearchCall{ID: "oai_search", Results: len(cited)}}, nil) {
				return
			}
		}
		stop := openaiStop(finish)
		if refused {
			stop = StopRefusal
		}
		yield(Part{Kind: PartStop, Stop: stop, Usage: &usage}, nil)
	}
}

// requestOptions adds web_search_options for WebSearch. The field max_uses
// is not in the OpenAI API, so only other servers get it.
func (m *OpenAIModel) requestOptions(r Request) []option.RequestOption {
	if !r.WebSearch {
		return nil
	}
	opts := map[string]any{"search_context_size": "low"}
	if u, err := url.Parse(m.cfg.BaseURL); m.cfg.BaseURL != "" && (err != nil || u.Hostname() != "api.openai.com") {
		opts["max_uses"] = webSearchUses
	}
	return []option.RequestOption{option.WithJSONSet("web_search_options", opts)}
}

func pc2call(pc *pendingCall) *ToolCall {
	c := &ToolCall{ID: pc.id, Name: pc.name, Input: json.RawMessage(`{}`)}
	if args := strings.TrimSpace(pc.args.String()); args != "" {
		if json.Valid([]byte(args)) {
			c.Input = json.RawMessage(args)
		} else {
			c.Input = nil
			c.Err = "The tool input is not valid JSON. The output may have been cut off."
		}
	}
	return c
}

func openaiStop(finish string) StopReason {
	switch finish {
	case "tool_calls", "function_call":
		return StopToolUse
	case "length":
		return StopMaxTokens
	case "content_filter":
		return StopRefusal
	}
	return StopEnd
}

func openaiError(err error) error {
	var ae *openai.Error
	if errors.As(err, &ae) {
		return &ProviderError{Status: ae.StatusCode, Err: err}
	}
	var se *ssestream.StreamError
	if errors.As(err, &se) {
		return &ProviderError{Err: err}
	}
	return err
}

func (m *OpenAIModel) params(r Request) (openai.ChatCompletionNewParams, error) {
	p := openai.ChatCompletionNewParams{
		Model:               r.Model,
		MaxCompletionTokens: param.NewOpt(int64(r.MaxTokens)),
		StreamOptions:       openai.ChatCompletionStreamOptionsParam{IncludeUsage: param.NewOpt(true)},
	}
	if r.Conversation != "" {
		p.User = param.NewOpt(r.Conversation)
	}
	if len(r.System) > 0 {
		p.Messages = append(p.Messages, openai.SystemMessage(strings.Join(r.System, "\n\n")))
	}
	for _, d := range r.Tools {
		var schema shared.FunctionParameters
		if err := json.Unmarshal(d.Schema, &schema); err != nil {
			return p, errors.New("assistant: tool " + d.Name + ": bad schema: " + err.Error())
		}
		p.Tools = append(p.Tools, openai.ChatCompletionFunctionTool(shared.FunctionDefinitionParam{
			Name:        d.Name,
			Description: openai.String(d.Description),
			Parameters:  schema,
		}))
	}
	if r.NoTools && len(p.Tools) > 0 {
		p.ToolChoice = openai.ChatCompletionToolChoiceOptionUnionParam{OfAuto: param.NewOpt("none")}
	}
	for _, m := range r.Messages {
		p.Messages = append(p.Messages, openaiMessages(m)...)
	}
	return p, nil
}

// openaiMessages converts a message. Tool results become one tool
// message each. Native parts are ignored.
func openaiMessages(m Message) []openai.ChatCompletionMessageParamUnion {
	var text []string
	var calls []openai.ChatCompletionMessageToolCallUnionParam
	var out []openai.ChatCompletionMessageParamUnion
	for _, p := range m.Parts {
		switch p.Kind {
		case PartText:
			text = append(text, p.Text)
		case PartToolCall:
			args := string(p.Call.Input)
			if args == "" {
				args = "{}"
			}
			calls = append(calls, openai.ChatCompletionMessageToolCallUnionParam{OfFunction: &openai.ChatCompletionMessageFunctionToolCallParam{
				ID:       p.Call.ID,
				Function: openai.ChatCompletionMessageFunctionToolCallFunctionParam{Name: p.Call.Name, Arguments: args},
			}})
		case PartToolResult:
			out = append(out, openai.ToolMessage(string(p.Result.Content), p.Result.CallID))
		}
	}
	joined := strings.Join(text, "")
	if m.Role == "assistant" {
		a := openai.ChatCompletionAssistantMessageParam{ToolCalls: calls}
		if joined != "" {
			a.Content.OfString = param.NewOpt(joined)
		}
		return append(out, openai.ChatCompletionMessageParamUnion{OfAssistant: &a})
	}
	if joined != "" {
		out = append(out, openai.UserMessage(joined))
	}
	return out
}
