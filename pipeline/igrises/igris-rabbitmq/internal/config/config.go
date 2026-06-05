package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

type Config struct {
	ProdURL                  string
	ShadowQueueName          string
	ShadowPublishExchange    string
	ShadowPublishExchangeType string
	ControlAURL              string
	ControlBURL              string
	CandidateURL             string
	Prefetch                 int
}

func Load() (Config, error) {
	cfg := Config{
		ProdURL:                   strings.TrimSpace(os.Getenv("PROD_URL")),
		ShadowQueueName:           strings.TrimSpace(os.Getenv("SHADOW_QUEUE_NAME")),
		ShadowPublishExchange:     strings.TrimSpace(os.Getenv("SHADOW_PUBLISH_EXCHANGE")),
		ShadowPublishExchangeType: strings.TrimSpace(os.Getenv("SHADOW_PUBLISH_EXCHANGE_TYPE")),
		ControlAURL:               strings.TrimSpace(os.Getenv("CONTROL_A_AMQP_URL")),
		ControlBURL:               strings.TrimSpace(os.Getenv("CONTROL_B_AMQP_URL")),
		CandidateURL:              strings.TrimSpace(os.Getenv("CANDIDATE_AMQP_URL")),
		Prefetch:                  10,
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
	if cfg.ProdURL == "" {
		return Config{}, fmt.Errorf("PROD_URL is required")
	}
	if cfg.ShadowQueueName == "" {
		return Config{}, fmt.Errorf("SHADOW_QUEUE_NAME is required")
	}
	if cfg.ShadowPublishExchange == "" {
		return Config{}, fmt.Errorf("SHADOW_PUBLISH_EXCHANGE is required")
	}
	if cfg.ControlAURL == "" || cfg.ControlBURL == "" || cfg.CandidateURL == "" {
		return Config{}, fmt.Errorf("CONTROL_A/B and CANDIDATE_AMQP_URL are required")
	}
	return cfg, nil
}
