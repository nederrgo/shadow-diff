package config

import "testing"

func requiredEnv(t *testing.T) {
	t.Helper()
	t.Setenv("PROD_URL", "amqp://prod:5672")
	t.Setenv("SHADOW_QUEUE_NAME", "q")
	t.Setenv("SHADOW_PUBLISH_EXCHANGE", "ex")
	t.Setenv("CONTROL_A_AMQP_URL", "amqp://a:5672")
	t.Setenv("CONTROL_B_AMQP_URL", "amqp://b:5672")
	t.Setenv("CANDIDATE_AMQP_URL", "amqp://c:5672")
}

func TestLoad_samplePercentageDefault(t *testing.T) {
	requiredEnv(t)
	t.Setenv("IGRIS_RMQ_SAMPLE_PERCENTAGE", "")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SamplePercentage != 100 {
		t.Fatalf("SamplePercentage = %d, want 100", cfg.SamplePercentage)
	}
}

func TestLoad_samplePercentageOverride(t *testing.T) {
	requiredEnv(t)
	t.Setenv("IGRIS_RMQ_SAMPLE_PERCENTAGE", "10")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SamplePercentage != 10 {
		t.Fatalf("SamplePercentage = %d, want 10", cfg.SamplePercentage)
	}
}

func TestLoad_samplePercentageInvalid(t *testing.T) {
	requiredEnv(t)
	t.Setenv("IGRIS_RMQ_SAMPLE_PERCENTAGE", "150")
	if _, err := Load(); err == nil {
		t.Fatal("expected error for out-of-range IGRIS_RMQ_SAMPLE_PERCENTAGE")
	}
}
