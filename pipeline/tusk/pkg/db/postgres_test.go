package db

import (
	"strings"
	"testing"
)

func TestConfigFromEnv_Absent(t *testing.T) {
	t.Setenv("DB_HOST", "")
	t.Setenv("DB_USER", "")
	t.Setenv("DB_NAME", "")
	if _, ok := ConfigFromEnv(); ok {
		t.Fatal("expected ok=false when DB_* missing")
	}
}

func TestConfigFromEnv_Present(t *testing.T) {
	t.Setenv("DB_HOST", "localhost")
	t.Setenv("DB_USER", "beru")
	t.Setenv("DB_NAME", "beru")
	t.Setenv("DB_PORT", "")
	t.Setenv("DB_SSLMODE", "")
	t.Setenv("DB_PASSWORD", "secret")
	cfg, ok := ConfigFromEnv()
	if !ok {
		t.Fatal("expected ok=true")
	}
	if cfg.Port != defaultPort || cfg.SSLMode != defaultSSLMode {
		t.Fatalf("defaults = %q/%q", cfg.Port, cfg.SSLMode)
	}
	dsn := cfg.DSN()
	for _, part := range []string{"localhost", "beru", "sslmode=require"} {
		if !strings.Contains(dsn, part) {
			t.Fatalf("dsn %q missing %q", dsn, part)
		}
	}
}

func TestConfigFromEnv_Partial(t *testing.T) {
	t.Setenv("DB_HOST", "localhost")
	t.Setenv("DB_USER", "")
	t.Setenv("DB_NAME", "beru")
	if _, ok := ConfigFromEnv(); ok {
		t.Fatal("partial config must be rejected")
	}
}
