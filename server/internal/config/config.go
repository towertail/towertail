package config

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/knadh/koanf/providers/env"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/v2"
)

// Config is the fully resolved server configuration. Values are loaded
// from (in order, last wins): defaults → YAML file at TT_CONFIG_FILE →
// environment variables with the TT_ prefix.
type Config struct {
	HTTP       HTTPConfig
	ClickHouse ClickHouseConfig
	Postgres   PostgresConfig
	Auth       AuthConfig
	Storage    StorageConfig
	Ingest     IngestConfig
	Log        LogConfig
	Cloud      CloudConfig
}

type HTTPConfig struct {
	Addr            string        `koanf:"addr"`
	ReadTimeout     time.Duration `koanf:"read_timeout"`
	WriteTimeout    time.Duration `koanf:"write_timeout"`
	ShutdownTimeout time.Duration `koanf:"shutdown_timeout"`
	// CORSAllowOrigins defaults to ["*"]. Tighten in production when the
	// Mac app never calls you — this is a defense-in-depth knob only,
	// since bearer tokens are required on every route anyway.
	CORSAllowOrigins []string `koanf:"cors_allow_origins"`
	// RateLimitRPS is requests-per-second per-IP on the generic limiter
	// (everything except /v1/ingest, which has its own budget).
	RateLimitRPS int `koanf:"rate_limit_rps"`
	// IngestRateLimitRPS allows samplers pushing metrics to exceed the
	// generic rate limit — a busy fleet can easily push > 10 rps/host.
	IngestRateLimitRPS int `koanf:"ingest_rate_limit_rps"`
}

type ClickHouseConfig struct {
	Addr             string        `koanf:"addr"`
	Database         string        `koanf:"database"`
	User             string        `koanf:"user"`
	Password         string        `koanf:"password"`
	PasswordFile     string        `koanf:"password_file"`
	DialTimeout      time.Duration `koanf:"dial_timeout"`
	MaxOpenConns     int           `koanf:"max_open_conns"`
	MaxIdleConns     int           `koanf:"max_idle_conns"`
	ConnMaxLifetime  time.Duration `koanf:"conn_max_lifetime"`
	BatchFlushRows   int           `koanf:"batch_flush_rows"`
	BatchFlushBytes  int           `koanf:"batch_flush_bytes"`
	BatchFlushPeriod time.Duration `koanf:"batch_flush_period"`
	RetentionDays    int           `koanf:"retention_days"`
}

type PostgresConfig struct {
	DSN          string `koanf:"dsn"`
	PasswordFile string `koanf:"password_file"`
	MaxConns     int    `koanf:"max_conns"`
}

type AuthConfig struct {
	// BootstrapToken, if set, is installed as the initial admin user
	// token at first boot. Rotated out afterwards.
	BootstrapToken string `koanf:"bootstrap_token"`
}

type StorageConfig struct {
	Path string `koanf:"path"`
}

type IngestConfig struct {
	MaxBodyBytes int64 `koanf:"max_body_bytes"`
}

type LogConfig struct {
	Level  string `koanf:"level"`
	Format string `koanf:"format"`
}

type CloudConfig struct {
	Managed bool `koanf:"managed"`
}

// Load resolves configuration from YAML + env. Path may be empty.
func Load(path string) (*Config, error) {
	k := koanf.New(".")
	setDefaults(k)

	if path != "" {
		if err := k.Load(file.Provider(path), yaml.Parser()); err != nil {
			return nil, fmt.Errorf("load config file %q: %w", path, err)
		}
	}

	// TT_FOO_BAR → foo.bar
	if err := k.Load(env.Provider("TT_", ".", func(s string) string {
		s = strings.TrimPrefix(s, "TT_")
		s = strings.ToLower(s)
		return strings.ReplaceAll(s, "__", ".")
	}), nil); err != nil {
		return nil, fmt.Errorf("load env: %w", err)
	}

	var cfg Config
	if err := k.Unmarshal("", &cfg); err != nil {
		return nil, fmt.Errorf("unmarshal: %w", err)
	}

	if err := cfg.resolveSecrets(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func setDefaults(k *koanf.Koanf) {
	_ = k.Set("http.addr", ":8080")
	_ = k.Set("http.read_timeout", "30s")
	_ = k.Set("http.write_timeout", "30s")
	_ = k.Set("http.shutdown_timeout", "10s")
	_ = k.Set("http.cors_allow_origins", []string{"*"})
	_ = k.Set("http.rate_limit_rps", 100)
	_ = k.Set("http.ingest_rate_limit_rps", 1000)

	_ = k.Set("clickhouse.addr", "clickhouse:9000")
	_ = k.Set("clickhouse.database", "towertail")
	_ = k.Set("clickhouse.user", "towertail")
	_ = k.Set("clickhouse.dial_timeout", "5s")
	_ = k.Set("clickhouse.max_open_conns", 16)
	_ = k.Set("clickhouse.max_idle_conns", 4)
	_ = k.Set("clickhouse.conn_max_lifetime", "10m")
	_ = k.Set("clickhouse.batch_flush_rows", 5000)
	_ = k.Set("clickhouse.batch_flush_bytes", 1048576)
	_ = k.Set("clickhouse.batch_flush_period", "1s")
	_ = k.Set("clickhouse.retention_days", 7)

	_ = k.Set("postgres.max_conns", 10)

	_ = k.Set("storage.path", "/var/lib/towertail")

	_ = k.Set("ingest.max_body_bytes", 16*1024*1024)

	_ = k.Set("log.level", "info")
	_ = k.Set("log.format", "json")

	_ = k.Set("cloud.managed", false)
}

func (c *Config) resolveSecrets() error {
	if c.ClickHouse.Password == "" && c.ClickHouse.PasswordFile != "" {
		b, err := os.ReadFile(c.ClickHouse.PasswordFile)
		if err != nil {
			return fmt.Errorf("read clickhouse password file: %w", err)
		}
		c.ClickHouse.Password = strings.TrimSpace(string(b))
	}
	if c.Postgres.DSN == "" && c.Postgres.PasswordFile != "" {
		// Caller can assemble DSN from parts; password file alone is not
		// enough. We accept the field for symmetry and leave assembly to
		// future work.
	}
	return nil
}
