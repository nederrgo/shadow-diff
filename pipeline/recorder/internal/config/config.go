package config

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"
)

const (
	defaultRecordAndReplayFile = "/etc/recorder/recordAndReplay.json"
	defaultListenAddr          = ":8080"
	defaultPairTimeout         = 30 * time.Second
	defaultMaxFrameBytes       = 5 << 20 // 5MB
)

// RecordAndReplayHost matches Monarch/Siphon record-and-replay entries.
type RecordAndReplayHost struct {
	Host        string   `json:"host"`
	IgnorePaths []string `json:"ignore_paths,omitempty"`
}

// Config holds Recorder process configuration.
type Config struct {
	ListenAddr         string
	BeruHTTPURL        string
	RecordAndReplay    []RecordAndReplayHost
	RecordAndReplayFile string
	PairTimeout        time.Duration
	MaxFrameBytes      int
}

// Load reads configuration from the environment and recordAndReplay file.
func Load() Config {
	cfg := Config{
		ListenAddr:          envOr("RECORDER_LISTEN_ADDR", defaultListenAddr),
		BeruHTTPURL:         strings.TrimSpace(os.Getenv("BERU_HTTP_URL")),
		RecordAndReplayFile: envOr("RECORDER_RECORD_AND_REPLAY_FILE", defaultRecordAndReplayFile),
		PairTimeout:         defaultPairTimeout,
		MaxFrameBytes:       defaultMaxFrameBytes,
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

	hosts, err := loadRecordAndReplay(cfg.RecordAndReplayFile)
	if err != nil {
		slog.Error("invalid recordAndReplay configuration", "err", err, "file", cfg.RecordAndReplayFile)
		os.Exit(1)
	}
	cfg.RecordAndReplay = hosts

	if cfg.BeruHTTPURL == "" {
		slog.Error("BERU_HTTP_URL is required")
		os.Exit(1)
	}
	if !strings.HasPrefix(cfg.BeruHTTPURL, "http://") && !strings.HasPrefix(cfg.BeruHTTPURL, "https://") {
		cfg.BeruHTTPURL = "http://" + cfg.BeruHTTPURL
	}
	cfg.BeruHTTPURL = strings.TrimSuffix(cfg.BeruHTTPURL, "/")

	return cfg
}

func loadRecordAndReplay(path string) ([]RecordAndReplayHost, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	raw = []byte(strings.TrimSpace(string(raw)))
	if len(raw) == 0 || string(raw) == "[]" {
		return nil, nil
	}
	var out []RecordAndReplayHost
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return out, nil
}

func envOr(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}
