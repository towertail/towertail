package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"slices"
	"strings"
)

// AssistantConfig is the config of the assistant. It comes from the env
// var TT_ASSISTANT, which holds one JSON object.
type AssistantConfig struct {
	Model         string            `json:"model"`
	APIFormat     string            `json:"api_format"` // "anthropic" | "openai"
	BaseURL       string            `json:"base_url"`
	APIKey        string            `json:"api_key"`
	APIKeyEnv     string            `json:"api_key_env"`
	MaxTokens     int               `json:"max_tokens"`     // default 8192
	ContextTokens int               `json:"context_tokens"` // default 100000
	Headers       map[string]string `json:"headers"`
	SystemExtra   string            `json:"system_extra"`
	WebSearch     bool              `json:"web_search"` // Anthropic only
	Effort        string            `json:"effort"`     // default "low"
	Limits        AssistantLimits   `json:"limits"`
}

// AssistantLimits holds the limits of the plans. A zero value means no
// limit.
type AssistantLimits struct {
	Default AssistantPlanLimits            `json:"default"`
	Plans   map[string]AssistantPlanLimits `json:"plans"` // keys: billing plan IDs
}

// AssistantPlanLimits are the limits of one plan.
type AssistantPlanLimits struct {
	Model                 string `json:"model,omitempty"`
	QuestionsPerDay       int    `json:"questions_per_day"`
	QuestionsPerMonth     int    `json:"questions_per_month"`
	QuestionsPerSeatMonth int    `json:"questions_per_seat_month"`
	MaxToolCalls          int    `json:"max_tool_calls"`
	QuestionTokens        int    `json:"question_tokens"`
	ReplyTokens           int    `json:"reply_tokens"`
	WebSearch             *bool  `json:"web_search,omitempty"`
	RetentionDays         int    `json:"retention_days"`
}

// ErrNoAssistantKey means that no source gives an API key.
var ErrNoAssistantKey = errors.New("config: no assistant API key")

const (
	defaultAssistantMaxTokens     = 8192
	defaultAssistantContextTokens = 100000
	defaultAssistantEffort        = "low"
)

var assistantEfforts = []string{"low", "medium", "high", "xhigh", "max"}

// loadAssistant reads TT_ASSISTANT. An empty value gives the zero config.
func loadAssistant(getenv func(string) string) (AssistantConfig, error) {
	var c AssistantConfig
	raw := strings.TrimSpace(getenv("TT_ASSISTANT"))
	if raw == "" {
		return c, nil
	}
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return AssistantConfig{}, fmt.Errorf("TT_ASSISTANT: %w", err)
	}
	if dec.More() {
		return AssistantConfig{}, errors.New("TT_ASSISTANT: more than one JSON value")
	}
	return c, nil
}

// Enabled tells if the assistant has a model and a key.
func (c AssistantConfig) Enabled() bool {
	if c.Model == "" {
		return false
	}
	_, _, err := c.ResolveKey(os.Getenv)
	return err == nil
}

// Format gives the API format. The default is anthropic.
func (c AssistantConfig) Format() string {
	if c.APIFormat == "" {
		return "anthropic"
	}
	return c.APIFormat
}

func (c AssistantConfig) EffortOrDefault() string {
	if c.Effort == "" {
		return defaultAssistantEffort
	}
	return c.Effort
}

func (c AssistantConfig) MaxTokensOrDefault() int {
	if c.MaxTokens == 0 {
		return defaultAssistantMaxTokens
	}
	return c.MaxTokens
}

func (c AssistantConfig) ContextTokensOrDefault() int {
	if c.ContextTokens == 0 {
		return defaultAssistantContextTokens
	}
	return c.ContextTokens
}

// ResolveKey gives the API key and a label of its source. The label never
// holds the key. The order is api_key, api_key_env, then ANTHROPIC_API_KEY
// or OPENAI_API_KEY by format.
func (c AssistantConfig) ResolveKey(getenv func(string) string) (key, source string, err error) {
	if k := strings.TrimSpace(c.APIKey); k != "" {
		return k, "config:api_key", nil
	}
	envs := []string{c.APIKeyEnv, "ANTHROPIC_API_KEY"}
	if c.Format() == "openai" {
		envs[1] = "OPENAI_API_KEY"
	}
	for _, name := range envs {
		if name == "" {
			continue
		}
		if k := strings.TrimSpace(getenv(name)); k != "" {
			return k, "env:" + name, nil
		}
	}
	return "", "", ErrNoAssistantKey
}

// PlanLimits gives the limits of a billing plan. A plan entry replaces
// Default as a whole, except an empty model, which takes the top-level
// model.
func (c AssistantConfig) PlanLimits(plan string) AssistantPlanLimits {
	p, ok := c.Limits.Plans[plan]
	if !ok {
		return c.Limits.Default
	}
	if p.Model == "" {
		p.Model = c.Model
	}
	return p
}

// Validate checks the values. It logs a warning for web_search with the
// openai format.
func (c AssistantConfig) Validate() error {
	switch c.APIFormat {
	case "", "anthropic", "openai":
	default:
		return fmt.Errorf("assistant.api_format %q must be anthropic or openai", c.APIFormat)
	}
	if c.Effort != "" && !slices.Contains(assistantEfforts, c.Effort) {
		return fmt.Errorf("assistant.effort %q must be one of %s", c.Effort, strings.Join(assistantEfforts, ", "))
	}
	if c.MaxTokens < 0 || c.ContextTokens < 0 {
		return errors.New("assistant.max_tokens and assistant.context_tokens must not be negative")
	}
	if err := c.Limits.Default.validate("limits.default"); err != nil {
		return err
	}
	for _, id := range slices.Sorted(maps.Keys(c.Limits.Plans)) {
		if err := c.Limits.Plans[id].validate("limits.plans." + id); err != nil {
			return err
		}
	}
	if c.Format() == "openai" && c.WebSearch {
		slog.Warn("assistant.web_search works only with the anthropic format")
	}
	return nil
}

func (p AssistantPlanLimits) validate(path string) error {
	for name, v := range map[string]int{
		"questions_per_day":        p.QuestionsPerDay,
		"questions_per_month":      p.QuestionsPerMonth,
		"questions_per_seat_month": p.QuestionsPerSeatMonth,
		"max_tool_calls":           p.MaxToolCalls,
		"question_tokens":          p.QuestionTokens,
		"reply_tokens":             p.ReplyTokens,
		"retention_days":           p.RetentionDays,
	} {
		if v < 0 {
			return fmt.Errorf("assistant.%s.%s must not be negative", path, name)
		}
	}
	if p.QuestionsPerMonth > 0 && p.QuestionsPerSeatMonth > 0 {
		return fmt.Errorf("assistant.%s sets both questions_per_month and questions_per_seat_month", path)
	}
	return nil
}

// LogValue hides the API key and the header values.
func (c AssistantConfig) LogValue() slog.Value {
	key := ""
	if c.APIKey != "" {
		key = "[hidden]"
	}
	return slog.GroupValue(
		slog.String("model", c.Model),
		slog.String("api_format", c.Format()),
		slog.String("base_url", c.BaseURL),
		slog.String("api_key", key),
		slog.String("api_key_env", c.APIKeyEnv),
		slog.Any("headers", slices.Sorted(maps.Keys(c.Headers))),
		slog.Bool("web_search", c.WebSearch),
		slog.String("effort", c.EffortOrDefault()),
	)
}
