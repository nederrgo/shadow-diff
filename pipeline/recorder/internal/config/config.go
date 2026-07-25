package config

import (
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"
)

const (
	defaultListenAddr    = ":8080"
	defaultOTLPGRPCAddr  = ":4317"
	defaultPairTimeout   = 30 * time.Second
	defaultMaxFrameBytes = 5 << 20 // 5MB
)

// Config holds Recorder process configuration.
type Config struct {
	ListenAddr       string
	OTLPGRPCAddr     string
	ShopHTTPURL      string
	PairTimeout      time.Duration
	MaxFrameBytes    int
	SamplePercentage int
}

// Load reads configuration from the environment.
func Load() Config {
	cfg := Config{
		ListenAddr:       envOr("RECORDER_LISTEN_ADDR", defaultListenAddr),
		OTLPGRPCAddr:     envOr("RECORDER_OTLP_GRPC_ADDR", defaultOTLPGRPCAddr),
		ShopHTTPURL:      strings.TrimSpace(os.Getenv("SHOP_HTTP_URL")),
		PairTimeout:      defaultPairTimeout,
		MaxFrameBytes:    defaultMaxFrameBytes,
		SamplePercentage: 100,
	}
	if v := os.Getenv("RECORDER_SAMPLE_PERCENTAGE"); v != "" {
		var n int
		if _, err := fmt.Sscanf(v, "%d", &n); err == nil && n > 0 {
			cfg.SamplePercentage = n
		}
	}
	if v := os.Getenv("RECORDER_PAIR_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			cfg.PairTimeout = d
		}
	}
	if v := os.Getenv("RECORDER_MAX_FRAME_BYTES"); v != "" {
		var n int
		if _, err := fmt.Sscanf(v, "%d", &n); err == nil && n > 0 {
			cfg.MaxFrameBytes = n
		}
	}

	if cfg.ShopHTTPURL == "" {
		slog.Error("SHOP_HTTP_URL is required")
		os.Exit(1)
	}
	if !strings.HasPrefix(cfg.ShopHTTPURL, "http://") && !strings.HasPrefix(cfg.ShopHTTPURL, "https://") {
		cfg.ShopHTTPURL = "http://" + cfg.ShopHTTPURL
	}
	cfg.ShopHTTPURL = strings.TrimSuffix(cfg.ShopHTTPURL, "/")

	return cfg
}

func envOr(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}
