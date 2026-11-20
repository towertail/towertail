package config

import (
	"os"
	"path/filepath"
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

func TestStreamDefaults(t *testing.T) {
	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Stream.LegacySamples {
		t.Error("legacy_samples must be true by default")
	}
	t.Setenv("TT_STREAM__LEGACY_SAMPLES", "false")
	cfg, err = Load("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Stream.LegacySamples {
		t.Error("TT_STREAM__LEGACY_SAMPLES=false did not apply")
	}
}

func TestStreamIntervalDefaults(t *testing.T) {
	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Stream.LiveInterval != 5*time.Second || cfg.Stream.IdleInterval != 30*time.Second {
		t.Errorf("intervals %v and %v", cfg.Stream.LiveInterval, cfg.Stream.IdleInterval)
	}
}

func TestStreamIntervalRejectsBadValues(t *testing.T) {
	for name, env := range map[string][2]string{
		"live under 1s":   {"TT_STREAM__LIVE_INTERVAL", "500ms"},
		"idle under live": {"TT_STREAM__IDLE_INTERVAL", "2s"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv(env[0], env[1])
			if _, err := Load(""); err == nil {
				t.Error("Load accepted the value")
			}
		})
	}
}

func TestStreamMaxLeasesRejectsNegative(t *testing.T) {
	t.Setenv("TT_STREAM__MAX_LEASES_PER_USER", "-1")
	if _, err := Load(""); err == nil {
		t.Error("Load accepted -1")
	}
}

func TestStreamMaxLeasesFromEnv(t *testing.T) {
	cfg, err := Load("")
	if err != nil || cfg.Stream.MaxLeasesPerUser != 0 {
		t.Fatalf("default: %v, %v", cfg.Stream.MaxLeasesPerUser, err)
	}
	t.Setenv("TT_STREAM__MAX_LEASES_PER_USER", "5")
	cfg, err = Load("")
	if err != nil || cfg.Stream.MaxLeasesPerUser != 5 {
		t.Fatalf("env: %v, %v", cfg.Stream.MaxLeasesPerUser, err)
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

func TestStreamCompressionDefault(t *testing.T) {
	cfg, err := Load("")
	if err != nil || cfg.Stream.Compression != "no_context_takeover" {
		t.Fatalf("default: %q, %v", cfg.Stream.Compression, err)
	}
	t.Setenv("TT_STREAM__COMPRESSION", "off")
	cfg, err = Load("")
	if err != nil || cfg.Stream.Compression != "off" {
		t.Fatalf("env: %q, %v", cfg.Stream.Compression, err)
	}
}

func TestStreamCompressionRejectsUnknown(t *testing.T) {
	for _, v := range []string{"context_takeover", "zstd"} {
		t.Setenv("TT_STREAM__COMPRESSION", v)
		if _, err := Load(""); err == nil {
			t.Errorf("%s: want error", v)
		}
	}
}
