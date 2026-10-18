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
