package config

import (
	"os"
	"testing"
)

func TestLoad(t *testing.T) {
	t.Setenv("CONTROL_A_AMQP_URL", "amqp://a")
	t.Setenv("CONTROL_B_AMQP_URL", "amqp://b")
	t.Setenv("CANDIDATE_AMQP_URL", "amqp://c")
	t.Setenv("BERU_HTTP_URL", "http://beru:8080")
	t.Setenv("SHADOW_TEST_NAME", "my-test")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.BeruHTTPURL != "http://beru:8080" {
		t.Fatalf("BeruHTTPURL = %s", cfg.BeruHTTPURL)
	}
	if cfg.ShadowTestName != "my-test" {
		t.Fatalf("ShadowTestName = %q", cfg.ShadowTestName)
	}
	if cfg.EgressExchange != "egress-events" {
		t.Fatalf("EgressExchange = %q", cfg.EgressExchange)
	}
}

func TestLoadMissingRequired(t *testing.T) {
	os.Unsetenv("CONTROL_A_AMQP_URL")
	os.Unsetenv("CONTROL_B_AMQP_URL")
	os.Unsetenv("CANDIDATE_AMQP_URL")
	os.Unsetenv("BERU_HTTP_URL")
	if _, err := Load(); err == nil {
		t.Fatal("expected error")
	}
}
