// Package assistant runs the embedded assistant: the model loop, the
// providers, the components and the change proposals. It has no routes.
package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"net/http"

	"github.com/towertail/towertail/server/config"
)

// Model streams one model request.
type Model interface {
	Stream(ctx context.Context, r Request) iter.Seq2[Part, error]
}

// Request is one model request. System and Tools hold stable text only.
type Request struct {
	Model     string
	System    []string
	Tools     []ToolDef // sorted by name
	Messages  []Message
	MaxTokens int
	WebSearch bool
	Effort    string
	// NoTools keeps the tools in the request but stops the model from
	// calling them (tool_choice none).
	NoTools bool
	// Conversation is a stable, opaque ID of the chat. A gateway can use it
	// to keep one session for each chat.
	Conversation string
}

// ToolDef is a client tool.
type ToolDef struct {
	Name, Description string
	Schema            json.RawMessage
}

// Message is one turn of the conversation.
type Message struct {
	Role  string // "user" | "assistant"
	Parts []Part
}

// PartKind tells which field of a Part is set.
type PartKind string

const (
	PartText       PartKind = "text"
	PartToolCall   PartKind = "tool_call"
	PartToolResult PartKind = "tool_result"
	PartNative     PartKind = "native"
	PartStop       PartKind = "stop"
	// PartSearch tells the turn about a web search. It is not stored.
	PartSearch PartKind = "search"
)

// StopReason tells why the model stopped.
type StopReason string

const (
	StopEnd       StopReason = "end"
	StopToolUse   StopReason = "tool_use"
	StopMaxTokens StopReason = "max_tokens"
	StopRefusal   StopReason = "refusal"
	StopPause     StopReason = "pause"
)

// Part is one piece of a message, or one event of a stream.
type Part struct {
	Kind   PartKind    `json:"kind"`
	Text   string      `json:"text,omitempty"`
	Call   *ToolCall   `json:"call,omitempty"`
	Result *ToolResult `json:"result,omitempty"`
	// Native is a provider block that the API needs back unchanged, for
	// example a thinking block or a server tool block.
	Native json.RawMessage `json:"native,omitempty"`
	Stop   StopReason      `json:"stop,omitempty"`
	Usage  *Usage          `json:"usage,omitempty"`
	Search *SearchCall     `json:"search,omitempty"`
}

// SearchCall is a web search of the provider. The start has Query. The
// result has Results or Err. A summary has no Query.
type SearchCall struct {
	ID      string `json:"id"`
	Query   string `json:"query,omitempty"`
	Results int    `json:"results,omitempty"`
	Err     string `json:"err,omitempty"`
}

// ToolCall is a tool call of the model. Err is set when the model gave
// input that is not valid JSON, for example when the output was cut off.
type ToolCall struct {
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input,omitempty"`
	Err   string          `json:"err,omitempty"`
}

// ToolResult answers a ToolCall.
type ToolResult struct {
	CallID  string          `json:"callId"`
	Content json.RawMessage `json:"content"`
	IsError bool            `json:"isError,omitempty"`
}

// Usage counts the tokens of one model request. Input does not include
// the cached tokens.
type Usage struct {
	Input      int `json:"input"`
	Output     int `json:"output"`
	CacheRead  int `json:"cacheRead"`
	CacheWrite int `json:"cacheWrite"`
	// WebSearches counts the web searches of the provider.
	WebSearches int `json:"webSearches,omitempty"`
}

// Add adds u2 to u.
func (u *Usage) Add(u2 Usage) {
	u.Input += u2.Input
	u.Output += u2.Output
	u.CacheRead += u2.CacheRead
	u.CacheWrite += u2.CacheWrite
	u.WebSearches += u2.WebSearches
}

// ProviderError is a failed provider request. Its text is for the log
// only, because it can hold provider details.
type ProviderError struct {
	Status int
	Err    error
}

func (e *ProviderError) Error() string {
	if e.Status != 0 {
		return fmt.Sprintf("assistant: provider status %d: %v", e.Status, e.Err)
	}
	return fmt.Sprintf("assistant: provider: %v", e.Err)
}

func (e *ProviderError) Unwrap() error { return e.Err }

// NewModel picks the provider from cfg.Format().
func NewModel(cfg config.AssistantConfig, getenv func(string) string, hc *http.Client) (Model, error) {
	key, _, err := cfg.ResolveKey(getenv)
	if err != nil {
		return nil, err
	}
	switch f := cfg.Format(); f {
	case "anthropic":
		return NewAnthropicModel(cfg, key, hc), nil
	case "openai":
		return NewOpenAIModel(cfg, key, hc), nil
	default:
		return nil, errors.New("assistant: unknown api_format " + f)
	}
}
