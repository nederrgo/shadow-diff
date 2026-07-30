package config

import "testing"

func TestLoad_recordMode(t *testing.T) {
	t.Setenv("OPERATING_MODE", "record")
	t.Setenv("PROD_URL", "amqp://prod:5672")
	t.Setenv("SHADOW_QUEUE_NAME", "q")
	t.Setenv("IGRIS_RMQ_SAMPLE_PERCENTAGE", "")
	t.Setenv("CONTROL_A_AMQP_URL", "")
	t.Setenv("CONTROL_B_AMQP_URL", "")
	t.Setenv("CANDIDATE_AMQP_URL", "")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SamplePercentage != 100 || cfg.OperatingMode != "record" {
		t.Fatalf("%+v", cfg)
	}
}

func TestLoad_replayRequiresBrokers(t *testing.T) {
	t.Setenv("OPERATING_MODE", "replay")
	t.Setenv("SHADOW_PUBLISH_EXCHANGE", "ex")
	t.Setenv("CONTROL_A_AMQP_URL", "")
	t.Setenv("CONTROL_B_AMQP_URL", "amqp://b:5672")
	t.Setenv("CANDIDATE_AMQP_URL", "amqp://c:5672")
	if _, err := Load(); err == nil {
		t.Fatal("expected error for missing CONTROL_A")
	}
}

func TestLoad_rejectsLive(t *testing.T) {
	t.Setenv("OPERATING_MODE", "live")
	if _, err := Load(); err == nil {
		t.Fatal("expected error for live mode")
	}
}

func TestLoad_samplePercentageOverride(t *testing.T) {
	t.Setenv("OPERATING_MODE", "record")
	t.Setenv("PROD_URL", "amqp://prod:5672")
	t.Setenv("SHADOW_QUEUE_NAME", "q")
	t.Setenv("IGRIS_RMQ_SAMPLE_PERCENTAGE", "10")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SamplePercentage != 10 {
		t.Fatalf("SamplePercentage = %d, want 10", cfg.SamplePercentage)
	}
}
