package config

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func TestLoadPasswordFiles(t *testing.T) {
	dir := t.TempDir()
	write := func(name, v string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(v), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	t.Setenv("TT_POSTGRES__DSN", "postgres://app@db/towertail")
	t.Setenv("TT_POSTGRES__PASSWORD_FILE", write("pg", "pg-secret\n"))
	t.Setenv("TT_CLICKHOUSE__PASSWORD_FILE", write("ch", "ch-secret\n"))
	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Postgres.Password != "pg-secret" {
		t.Errorf("postgres password: %q", cfg.Postgres.Password)
	}
	if cfg.ClickHouse.Password != "ch-secret" {
		t.Errorf("clickhouse password: %q", cfg.ClickHouse.Password)
	}

	t.Setenv("TT_POSTGRES__PASSWORD", "from-env")
	cfg, err = Load("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Postgres.Password != "from-env" {
		t.Errorf("password wins over the file: %q", cfg.Postgres.Password)
	}
}

func TestCORSCredentials(t *testing.T) {
	t.Setenv("TT_HTTP__CORS_ALLOW_CREDENTIALS", "true")
	if _, err := Load(""); err == nil {
		t.Error("credentials with the origin *: want error")
	}
	t.Setenv("TT_HTTP__CORS_ALLOW_ORIGINS", "https://app.example.com")
	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.HTTP.CORSAllowOrigins) != 1 || cfg.HTTP.CORSAllowOrigins[0] != "https://app.example.com" {
		t.Errorf("origins: %v", cfg.HTTP.CORSAllowOrigins)
	}
}

func TestLoadSection(t *testing.T) {
	t.Setenv("TT_CLOUD__BASE_URL", "https://api.example.com")
	t.Setenv("TT_CLOUD__OAUTH__GITHUB__CLIENT_ID", "gh")
	var out struct {
		BaseURL string `koanf:"base_url"`
		OAuth   struct {
			GitHub struct {
				ClientID string `koanf:"client_id"`
			} `koanf:"github"`
		} `koanf:"oauth"`
	}
	if err := LoadSection("", "cloud", &out); err != nil {
		t.Fatal(err)
	}
	if out.BaseURL != "https://api.example.com" || out.OAuth.GitHub.ClientID != "gh" {
		t.Errorf("section: %+v", out)
	}
}

func TestStreamConfig(t *testing.T) {
	def := StreamConfig{LegacySamples: true, LiveInterval: 5 * time.Second, IdleInterval: 30 * time.Second, Compression: "no_context_takeover"}
	with := func(f func(*StreamConfig)) StreamConfig {
		c := def
		f(&c)
		return c
	}
	for _, c := range []struct {
		name string
		env  map[string]string
		want StreamConfig
	}{
		{"defaults", nil, def},
		{"legacy samples off", map[string]string{"TT_STREAM__LEGACY_SAMPLES": "false"}, with(func(c *StreamConfig) { c.LegacySamples = false })},
		{"max leases", map[string]string{"TT_STREAM__MAX_LEASES_PER_USER": "5"}, with(func(c *StreamConfig) { c.MaxLeasesPerUser = 5 })},
		{"compression off", map[string]string{"TT_STREAM__COMPRESSION": "off"}, with(func(c *StreamConfig) { c.Compression = "off" })},
	} {
		t.Run(c.name, func(t *testing.T) {
			for k, v := range c.env {
				t.Setenv(k, v)
			}
			cfg, err := Load("")
			if err != nil {
				t.Fatal(err)
			}
			if cfg.Stream != c.want {
				t.Errorf("stream = %+v, want %+v", cfg.Stream, c.want)
			}
		})
	}
}

func TestStreamConfigRejects(t *testing.T) {
	for _, env := range [][2]string{
		{"TT_STREAM__LIVE_INTERVAL", "500ms"},
		{"TT_STREAM__IDLE_INTERVAL", "2s"},
		{"TT_STREAM__MAX_LEASES_PER_USER", "-1"},
		{"TT_STREAM__COMPRESSION", "context_takeover"},
		{"TT_STREAM__COMPRESSION", "zstd"},
	} {
		t.Run(env[0]+"="+env[1], func(t *testing.T) {
			t.Setenv(env[0], env[1])
			if _, err := Load(""); err == nil {
				t.Error("Load accepted the value")
			}
		})
	}
}

func TestMCPEnabled(t *testing.T) {
	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.MCP.Enabled {
		t.Error("mcp.enabled default is false, want true")
	}
	t.Setenv("TT_MCP__ENABLED", "false")
	if cfg, err = Load(""); err != nil || cfg.MCP.Enabled {
		t.Errorf("TT_MCP__ENABLED=false: enabled = %v, err = %v", cfg.MCP.Enabled, err)
	}
}

func TestMCPOAuthConfig(t *testing.T) {
	t.Setenv("TT_MCP__RESOURCE_URL", "https://api.example.com/mcp")
	if _, err := Load(""); err == nil {
		t.Error("only resource_url: want error")
	}
	t.Setenv("TT_MCP__AUTHORIZATION_SERVER", "ftp://api.example.com")
	if _, err := Load(""); err == nil {
		t.Error("bad scheme: want error")
	}
	t.Setenv("TT_MCP__AUTHORIZATION_SERVER", "https://api.example.com")
	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.MCP.OAuth() {
		t.Error("OAuth() = false, want true")
	}
	want := []string{"https://api.example.com/mcp", "https://api.example.com/mcp/v1"}
	if got := cfg.MCP.Resources(); !slices.Equal(got, want) {
		t.Errorf("Resources() = %v, want %v", got, want)
	}
	if cfg, err = Load(""); err != nil || (MCPConfig{}).OAuth() {
		t.Errorf("empty config: err = %v", err)
	}
}

func TestLoadAssistantFromEnv(t *testing.T) {
	cfg, err := Load("")
	if err != nil || cfg.Assistant.Enabled() {
		t.Fatalf("no TT_ASSISTANT: err = %v, enabled = %v", err, cfg.Assistant.Enabled())
	}
	t.Setenv("TT_ASSISTANT", `{"model":"m1","api_key":"fake-key","effort":"high","limits":{"plans":{"pro":{"questions_per_day":9}}}}`)
	cfg, err = Load("")
	if err != nil {
		t.Fatal(err)
	}
	a := cfg.Assistant
	if a.Model != "m1" || a.EffortOrDefault() != "high" || !a.Enabled() || a.PlanLimits("pro").QuestionsPerDay != 9 {
		t.Errorf("assistant = %+v", a)
	}
	for _, bad := range []string{`{"model":`, `{"nope":1}`, `{"api_format":"gemini"}`} {
		t.Setenv("TT_ASSISTANT", bad)
		if _, err := Load(""); err == nil {
			t.Errorf("TT_ASSISTANT=%s: want error", bad)
		}
	}
}

func TestLoadAssistantNoKeyIsOff(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("TT_ASSISTANT", `{"model":"m1"}`)
	cfg, err := Load("")
	if err != nil || cfg.Assistant.Enabled() {
		t.Errorf("err = %v, enabled = %v; want no error and off", err, cfg.Assistant.Enabled())
	}
}
