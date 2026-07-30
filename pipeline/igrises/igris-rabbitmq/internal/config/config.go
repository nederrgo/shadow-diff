package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/shadow-diff/s3utils"
)

type Config struct {
	OperatingMode             string
	AdminAddr                 string
	ProdURL                   string
	ShadowQueueName           string
	ShadowPublishExchange     string
	ShadowPublishExchangeType string
	ControlAURL               string
	ControlBURL               string
	CandidateURL              string
	Prefetch                  int
	SamplePercentage          int
}

func Load() (Config, error) {
	mode, err := s3utils.RequireOperatingMode()
	if err != nil {
		return Config{}, err
	}
	cfg := Config{
		OperatingMode:             mode,
		AdminAddr:                 envOr("IGRIS_ADMIN_ADDR", ":9090"),
		ProdURL:                   strings.TrimSpace(os.Getenv("PROD_URL")),
		ShadowQueueName:           strings.TrimSpace(os.Getenv("SHADOW_QUEUE_NAME")),
		ShadowPublishExchange:     strings.TrimSpace(os.Getenv("SHADOW_PUBLISH_EXCHANGE")),
		ShadowPublishExchangeType: strings.TrimSpace(os.Getenv("SHADOW_PUBLISH_EXCHANGE_TYPE")),
		ControlAURL:               strings.TrimSpace(os.Getenv("CONTROL_A_AMQP_URL")),
		ControlBURL:               strings.TrimSpace(os.Getenv("CONTROL_B_AMQP_URL")),
		CandidateURL:              strings.TrimSpace(os.Getenv("CANDIDATE_AMQP_URL")),
		Prefetch:                  10,
		SamplePercentage:          100,
	}
	if cfg.ShadowPublishExchangeType == "" {
		cfg.ShadowPublishExchangeType = "topic"
	}
	if v := strings.TrimSpace(os.Getenv("PREFETCH")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			return Config{}, fmt.Errorf("invalid PREFETCH %q", v)
		}
		cfg.Prefetch = n
	}
	if v := strings.TrimSpace(os.Getenv("IGRIS_RMQ_SAMPLE_PERCENTAGE")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 100 {
			return Config{}, fmt.Errorf("invalid IGRIS_RMQ_SAMPLE_PERCENTAGE %q", v)
		}
		cfg.SamplePercentage = n
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c Config) Validate() error {
	switch c.OperatingMode {
	case s3utils.ModeRecord:
		if c.ProdURL == "" {
			return fmt.Errorf("PROD_URL is required in record mode")
		}
		if c.ShadowQueueName == "" {
			return fmt.Errorf("SHADOW_QUEUE_NAME is required in record mode")
		}
	case s3utils.ModeReplay:
		if c.ShadowPublishExchange == "" {
			return fmt.Errorf("SHADOW_PUBLISH_EXCHANGE is required in replay mode")
		}
		if c.ControlAURL == "" || c.ControlBURL == "" || c.CandidateURL == "" {
			return fmt.Errorf("CONTROL_A/B and CANDIDATE_AMQP_URL are required in replay mode")
		}
	default:
		return fmt.Errorf("OPERATING_MODE must be record or replay, got %q", c.OperatingMode)
	}
	return nil
}

func envOr(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}
