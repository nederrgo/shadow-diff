package config

import (
	"testing"

	"github.com/shadow-diff/shadow-soldier/internal/parsers"
	"github.com/shadow-diff/shadow-soldier/internal/proxy"
)

func routes() []proxy.Route {
	return []proxy.Route{{
		Protocol: parsers.ProtocolMongoDB,
		Listen:   27017,
		Upstream: "mongodb-control-a.shadow-default-t.svc.cluster.local:27017",
	}}
}

func TestLoad(t *testing.T) {
	t.Setenv("SHADOW_ROLE", "candidate")
	t.Setenv("SHADOW_TEST_NAME", "my-test")
	t.Setenv("POD_NAME", "my-test-candidate-abc")
	t.Setenv("BERU_HTTP_URL", "http://beru-local.shadow-default-t.svc.cluster.local:8081")
	t.Setenv("SOLDIER_ROUTES", `[{"protocol":"mongodb","listen":27017,"upstream":"mongodb-control-a:27017"}]`)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Role != "candidate" || len(cfg.Routes) != 1 {
		t.Fatalf("cfg = %+v", cfg)
	}
	if cfg.Routes[0].Upstream != "mongodb-control-a:27017" {
		t.Fatalf("upstream = %q", cfg.Routes[0].Upstream)
	}
	if cfg.MemoryLimit != defaultMemoryLimit || cfg.MaxConns != defaultMaxConns {
		t.Fatalf("defaults not applied: %+v", cfg)
	}
}

func TestLoadRequiresRoutes(t *testing.T) {
	t.Setenv("SHADOW_ROLE", "candidate")
	t.Setenv("BERU_HTTP_URL", "http://beru-local:8081")
	t.Setenv("SOLDIER_ROUTES", "")
	if _, err := Load(); err == nil {
		t.Fatal("Load = nil error with no routes, want error")
	}
}

func TestLoadRejectsBadRoutesJSON(t *testing.T) {
	t.Setenv("SHADOW_ROLE", "candidate")
	t.Setenv("BERU_HTTP_URL", "http://beru-local:8081")
	t.Setenv("SOLDIER_ROUTES", "not json")
	if _, err := Load(); err == nil {
		t.Fatal("Load = nil error with malformed routes, want error")
	}
}

func TestValidate(t *testing.T) {
	t.Parallel()
	base := Config{Role: "control-a", BeruURL: "http://beru-local:8081", Routes: routes()}

	if err := base.Validate(); err != nil {
		t.Fatalf("Validate(valid) = %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*Config)
	}{
		{"empty role", func(c *Config) { c.Role = "" }},
		// Beru validates the role at ingest and rejects anything else, so a typo
		// here would surface as silently missing reports.
		{"unknown role", func(c *Config) { c.Role = "control-c" }},
		{"missing beru url", func(c *Config) { c.BeruURL = "" }},
		{"unsupported protocol", func(c *Config) { c.Routes[0].Protocol = "cassandra" }},
		{"port out of range", func(c *Config) { c.Routes[0].Listen = 0 }},
		{"empty upstream", func(c *Config) { c.Routes[0].Upstream = "" }},
	}
	for _, tc := range tests {
		cfg := base
		cfg.Routes = routes()
		tc.mutate(&cfg)
		if err := cfg.Validate(); err == nil {
			t.Fatalf("%s: Validate = nil error, want error", tc.name)
		}
	}
}

// Two dependencies cannot share a loopback port — the second listener would fail
// to bind at startup, leaving one dependency unproxied and unreported.
func TestValidateRejectsDuplicateListenPort(t *testing.T) {
	t.Parallel()
	cfg := Config{
		Role:    "control-a",
		BeruURL: "http://beru-local:8081",
		Routes: []proxy.Route{
			{Protocol: parsers.ProtocolMongoDB, Listen: 6379, Upstream: "mongo:27017"},
			{Protocol: parsers.ProtocolRedis, Listen: 6379, Upstream: "redis:6379"},
		},
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate = nil error for duplicate listen ports, want error")
	}
}
