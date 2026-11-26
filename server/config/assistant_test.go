package config

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"
)

func getenvOf(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestLoadAssistant(t *testing.T) {
	for _, tc := range []struct {
		name    string
		env     string
		wantErr bool
		model   string
	}{
		{"empty", "", false, ""},
		{"valid", `{"model":"m1","api_format":"openai","limits":{"plans":{"pro":{"questions_per_day":5}}}}`, false, "m1"},
		{"unknown field", `{"model":"m1","nope":1}`, true, ""},
		{"bad json", `{"model":`, true, ""},
		{"two values", `{} {}`, true, ""},
	} {
		c, err := loadAssistant(getenvOf(map[string]string{"TT_ASSISTANT": tc.env}))
		if (err != nil) != tc.wantErr {
			t.Errorf("%s: err = %v, want error %v", tc.name, err, tc.wantErr)
		}
		if c.Model != tc.model {
			t.Errorf("%s: model = %q, want %q", tc.name, c.Model, tc.model)
		}
		if tc.env == "" && (c.Enabled() || err != nil) {
			t.Errorf("%s: empty env must turn the assistant off with no error", tc.name)
		}
	}
}

func TestAssistantResolveKey(t *testing.T) {
	all := map[string]string{"OWN": "own-key", "ANTHROPIC_API_KEY": "anthropic-key", "OPENAI_API_KEY": "openai-key"}
	for _, tc := range []struct {
		name       string
		c          AssistantConfig
		env        map[string]string
		wantKey    string
		wantSource string
		wantErr    error
	}{
		{"api_key wins", AssistantConfig{APIKey: "inline", APIKeyEnv: "OWN"}, all, "inline", "config:api_key", nil},
		{"api_key_env wins", AssistantConfig{APIKeyEnv: "OWN"}, all, "own-key", "env:OWN", nil},
		{"anthropic default", AssistantConfig{}, all, "anthropic-key", "env:ANTHROPIC_API_KEY", nil},
		{"openai default", AssistantConfig{APIFormat: "openai"}, all, "openai-key", "env:OPENAI_API_KEY", nil},
		{"empty api_key_env var falls through", AssistantConfig{APIKeyEnv: "OWN"}, map[string]string{"ANTHROPIC_API_KEY": "anthropic-key"}, "anthropic-key", "env:ANTHROPIC_API_KEY", nil},
		{"no key", AssistantConfig{}, nil, "", "", ErrNoAssistantKey},
		{"wrong format env", AssistantConfig{APIFormat: "openai"}, map[string]string{"ANTHROPIC_API_KEY": "anthropic-key"}, "", "", ErrNoAssistantKey},
	} {
		key, source, err := tc.c.ResolveKey(getenvOf(tc.env))
		if !errors.Is(err, tc.wantErr) || key != tc.wantKey || source != tc.wantSource {
			t.Errorf("%s: got (%q, %q, %v), want (%q, %q, %v)", tc.name, key, source, err, tc.wantKey, tc.wantSource, tc.wantErr)
		}
		if key != "" && strings.Contains(source, key) {
			t.Errorf("%s: source %q holds the key", tc.name, source)
		}
	}
}

func TestAssistantEnabled(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	if (AssistantConfig{Model: "m", APIKey: "k"}).Enabled() != true {
		t.Error("model and key: want enabled")
	}
	if (AssistantConfig{APIKey: "k"}).Enabled() {
		t.Error("no model: want disabled")
	}
	if (AssistantConfig{Model: "m"}).Enabled() {
		t.Error("no key: want disabled")
	}
	t.Setenv("ANTHROPIC_API_KEY", "fake")
	if !(AssistantConfig{Model: "m"}).Enabled() {
		t.Error("default env key: want enabled")
	}
}

func TestAssistantDefaults(t *testing.T) {
	var c AssistantConfig
	if c.Format() != "anthropic" || c.EffortOrDefault() != "low" || c.MaxTokensOrDefault() != 8192 || c.ContextTokensOrDefault() != 100000 {
		t.Errorf("defaults: %q %q %d %d", c.Format(), c.EffortOrDefault(), c.MaxTokensOrDefault(), c.ContextTokensOrDefault())
	}
	c = AssistantConfig{APIFormat: "openai", Effort: "high", MaxTokens: 1, ContextTokens: 2}
	if c.Format() != "openai" || c.EffortOrDefault() != "high" || c.MaxTokensOrDefault() != 1 || c.ContextTokensOrDefault() != 2 {
		t.Errorf("set values: %q %q %d %d", c.Format(), c.EffortOrDefault(), c.MaxTokensOrDefault(), c.ContextTokensOrDefault())
	}
}

func TestAssistantPlanLimits(t *testing.T) {
	c := AssistantConfig{Model: "top", Limits: AssistantLimits{
		Default: AssistantPlanLimits{Model: "def", QuestionsPerDay: 3, MaxToolCalls: 4},
		Plans: map[string]AssistantPlanLimits{
			"pro":  {Model: "big", QuestionsPerDay: 50},
			"team": {QuestionsPerMonth: 100},
		},
	}}
	for _, tc := range []struct {
		plan  string
		model string
		day   int
		tools int
	}{
		{"pro", "big", 50, 0},
		{"team", "top", 0, 0},
		{"unknown", "def", 3, 4},
		{"", "def", 3, 4},
	} {
		got := c.PlanLimits(tc.plan)
		if got.Model != tc.model || got.QuestionsPerDay != tc.day || got.MaxToolCalls != tc.tools {
			t.Errorf("%q: got %+v, want model %q day %d tools %d", tc.plan, got, tc.model, tc.day, tc.tools)
		}
	}
}

func TestAssistantValidate(t *testing.T) {
	for _, tc := range []struct {
		name string
		c    AssistantConfig
		want string
	}{
		{"empty", AssistantConfig{}, ""},
		{"valid", AssistantConfig{APIFormat: "openai", Effort: "medium", MaxTokens: 1}, ""},
		{"bad format", AssistantConfig{APIFormat: "gemini"}, "api_format"},
		{"bad effort", AssistantConfig{Effort: "extreme"}, "effort"},
		{"negative max tokens", AssistantConfig{MaxTokens: -1}, "negative"},
		{"negative context tokens", AssistantConfig{ContextTokens: -1}, "negative"},
		{"negative default limit", AssistantConfig{Limits: AssistantLimits{Default: AssistantPlanLimits{ReplyTokens: -1}}}, "limits.default.reply_tokens"},
		{"negative plan limit", AssistantConfig{Limits: AssistantLimits{Plans: map[string]AssistantPlanLimits{"pro": {RetentionDays: -1}}}}, "limits.plans.pro.retention_days"},
		{"month and seat month", AssistantConfig{Limits: AssistantLimits{Plans: map[string]AssistantPlanLimits{"pro": {QuestionsPerMonth: 1, QuestionsPerSeatMonth: 1}}}}, "both"},
	} {
		err := tc.c.Validate()
		switch {
		case tc.want == "" && err != nil:
			t.Errorf("%s: unexpected error %v", tc.name, err)
		case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
			t.Errorf("%s: err = %v, want text %q", tc.name, err, tc.want)
		}
	}
}

func TestAssistantValidateWarnsWebSearchOpenAI(t *testing.T) {
	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })
	if err := (AssistantConfig{APIFormat: "openai", WebSearch: true}).Validate(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "web_search") {
		t.Errorf("log = %q, want a web_search warning", buf.String())
	}
	buf.Reset()
	if err := (AssistantConfig{APIFormat: "openai", WebSearch: true, WebSearchSupport: WebSearchAlways}).Validate(); err != nil {
		t.Fatal(err)
	}
	if buf.Len() != 0 {
		t.Errorf("log = %q, want no warning with web_search_support always", buf.String())
	}
}

func TestAssistantWebSearchSupportValues(t *testing.T) {
	for _, v := range []string{WebSearchAuto, WebSearchAlways, WebSearchNever} {
		if err := (AssistantConfig{WebSearchSupport: v}).Validate(); err != nil {
			t.Errorf("%q: %v", v, err)
		}
	}
	err := (AssistantConfig{WebSearchSupport: "sometimes"}).Validate()
	if err == nil || !strings.Contains(err.Error(), "web_search_support") {
		t.Errorf("err = %v, want a web_search_support error", err)
	}
	c, err := loadAssistant(getenvOf(map[string]string{"TT_ASSISTANT": `{"web_search_support":"always"}`}))
	if err != nil || c.WebSearchSupport != WebSearchAlways {
		t.Errorf("load: %+v %v", c, err)
	}
}

func TestAssistantLogValueHidesSecrets(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	c := AssistantConfig{Model: "m", APIKey: "sk-fake-secret", Headers: map[string]string{"X-Auth": "header-secret"}}
	log.Info("assistant", "assistant", c)
	out := buf.String()
	for _, secret := range []string{"sk-fake-secret", "header-secret"} {
		if strings.Contains(out, secret) {
			t.Errorf("log %q holds %q", out, secret)
		}
	}
	if !strings.Contains(out, "X-Auth") || !strings.Contains(out, "model=m") {
		t.Errorf("log %q lacks the model or the header name", out)
	}
}
