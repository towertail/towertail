package config

import (
	"os"
	"path/filepath"
	"testing"
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
