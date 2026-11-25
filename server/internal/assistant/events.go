package assistant

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Event names. D5 sends each as one SSE event.
const (
	EventStep      = "step"
	EventText      = "text"
	EventComponent = "component"
	EventProposal  = "proposal"
	EventUsage     = "usage"
	EventDone      = "done"
	EventError     = "error"
	// EventQuestion is stored with the user message. It is never streamed.
	EventQuestion = "question"
)

// Error codes.
const (
	CodeAssistantOff  = "assistant_off"
	CodeDailyLimit    = "daily_limit"
	CodePlanLimit     = "plan_limit"
	CodeToolLimit     = "tool_limit"
	CodeQuestionToken = "question_tokens"
	CodeProviderError = "provider_error"
	CodeRefused       = "refused"
)

// Event is one event of an answer. Data is JSON.
type Event struct {
	Name string          `json:"name"`
	Data json.RawMessage `json:"data"`
}

func newEvent(name string, v any) Event {
	b, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("assistant: event %s: %v", name, err))
	}
	return Event{Name: name, Data: b}
}

// Decode reads the data of the event into v.
func (e Event) Decode(v any) error { return json.Unmarshal(e.Data, v) }

// StepEvent shows a read tool call. The first event has the status
// "running". The second has "done" or "error", with ms and rows.
type StepEvent struct {
	ID     string `json:"id"`
	Tool   string `json:"tool,omitempty"`
	Title  string `json:"title,omitempty"`
	Status string `json:"status"`
	MS     int64  `json:"ms,omitempty"`
	Rows   int    `json:"rows,omitempty"`
}

// TextEvent is a piece of the reply text.
type TextEvent struct {
	Delta string `json:"delta"`
}

// QuestionEvent keeps the question with the stored user message.
type QuestionEvent struct {
	Text     string      `json:"text"`
	Context  PageContext `json:"context"`
	Mentions []string    `json:"mentions,omitempty"`
}

// WindowUsage is the use of one limit window.
type WindowUsage struct {
	Used    int       `json:"used"`
	Limit   int       `json:"limit"`
	ResetAt time.Time `json:"resetAt,omitzero"`
	Pooled  bool      `json:"pooled,omitempty"`
}

// UsageEvent gives the question counts after the question.
type UsageEvent struct {
	Day   WindowUsage `json:"day"`
	Month WindowUsage `json:"month"`
}

// DoneStats are the totals of one question.
type DoneStats struct {
	MS        int64 `json:"ms"`
	ToolCalls int   `json:"toolCalls"`
	Rows      int   `json:"rows"`
	Input     int   `json:"input"`
	Output    int   `json:"output"`
	CacheRead int   `json:"cacheRead"`
}

// DoneEvent ends a stream that worked.
type DoneEvent struct {
	ChatID    uuid.UUID  `json:"chatId"`
	MessageID uuid.UUID  `json:"messageId"`
	Stop      StopReason `json:"stop"`
	Stats     DoneStats  `json:"stats"`
}

// ErrorEvent ends a stream that failed.
type ErrorEvent struct {
	Code    string    `json:"code"`
	Message string    `json:"message"`
	ResetAt time.Time `json:"resetAt,omitzero"`
}

// Error is a question that stopped before the stream began, or an error
// event. D5 turns it into an HTTP error.
type Error struct {
	Code    string
	Message string
	ResetAt time.Time
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }
