package config

import (
	"fmt"
	"os"
	"strings"
	"time"
)

const (
	defaultBeruEgressDiffPath = "/api/v1/egress/diff"
	defaultReconnectMin       = time.Second
	defaultReconnectMax       = 30 * time.Second
)

// Config holds runtime settings for the egress relay.
type Config struct {
	ControlAURL       string
	ControlBURL       string
	CandidateURL      string
	BeruHTTPURL       string
	BeruEgressDiffPath string
	ReconnectMin      time.Duration
	ReconnectMax      time.Duration
}

// Load reads configuration from environment variables.
func Load() (Config, error) {
	cfg := Config{
		ControlAURL:        strings.TrimSpace(os.Getenv("CONTROL_A_AMQP_URL")),
		ControlBURL:        strings.TrimSpace(os.Getenv("CONTROL_B_AMQP_URL")),
		CandidateURL:       strings.TrimSpace(os.Getenv("CANDIDATE_AMQP_URL")),
		BeruHTTPURL:        strings.TrimRight(strings.TrimSpace(os.Getenv("BERU_HTTP_URL")), "/"),
		BeruEgressDiffPath: defaultBeruEgressDiffPath,
		ReconnectMin:       defaultReconnectMin,
		ReconnectMax:       defaultReconnectMax,
	}
	if v := strings.TrimSpace(os.Getenv("BERU_EGRESS_DIFF_PATH")); v != "" {
		cfg.BeruEgressDiffPath = v
	}
	if v := strings.TrimSpace(os.Getenv("RECONNECT_MIN_DELAY")); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return Config{}, fmt.Errorf("RECONNECT_MIN_DELAY: %w", err)
		}
		cfg.ReconnectMin = d
	}
	if v := strings.TrimSpace(os.Getenv("RECONNECT_MAX_DELAY")); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return Config{}, fmt.Errorf("RECONNECT_MAX_DELAY: %w", err)
		}
		cfg.ReconnectMax = d
	}
	if cfg.ControlAURL == "" || cfg.ControlBURL == "" || cfg.CandidateURL == "" {
		return Config{}, fmt.Errorf("CONTROL_A_AMQP_URL, CONTROL_B_AMQP_URL, and CANDIDATE_AMQP_URL are required")
	}
	if cfg.BeruHTTPURL == "" {
		return Config{}, fmt.Errorf("BERU_HTTP_URL is required")
	}
	if !strings.HasPrefix(cfg.BeruEgressDiffPath, "/") {
		cfg.BeruEgressDiffPath = "/" + cfg.BeruEgressDiffPath
	}
	return cfg, nil
}

// BeruEgressDiffURL returns the full Beru egress diff endpoint.
func (c Config) BeruEgressDiffURL() string {
	return c.BeruHTTPURL + c.BeruEgressDiffPath
}
