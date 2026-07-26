// Package config loads shadow-soldier's environment configuration.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/shadow-diff/shadow-soldier/internal/beru"
	"github.com/shadow-diff/shadow-soldier/internal/proxy"
)

// Defaults chosen for a sidecar sized at ~32Mi: small enough to be invisible
// next to the application container, large enough for a normal connection pool.
const (
	defaultMemoryLimit = 24 << 20 // 24 MiB
	defaultMaxConns    = 512
	defaultDialTimeout = 5 * time.Second
	defaultIdleTimeout = 5 * time.Minute
	defaultTapChunks   = 256
	defaultHTTPTimeout = 5 * time.Second
)

// Config is the full runtime configuration.
type Config struct {
	Routes         []proxy.Route
	Role           string
	ShadowTestName string
	ShadowPod      string
	BeruURL        string

	MemoryLimit int64
	MaxConns    int
	QueueSize   int
	Workers     int
	DialTimeout time.Duration
	IdleTimeout time.Duration
	TapChunks   int
	HTTPTimeout time.Duration
}

// validRoles mirrors Beru's roles package. A report carrying anything else is
// rejected at ingest, so it is worth failing fast at startup instead.
var validRoles = map[string]struct{}{
	"control-a": {}, "control-b": {}, "candidate": {},
}

// Load reads the environment and validates it.
func Load() (Config, error) {
	cfg := Config{
		Role:           strings.TrimSpace(os.Getenv("SHADOW_ROLE")),
		ShadowTestName: strings.TrimSpace(os.Getenv("SHADOW_TEST_NAME")),
		ShadowPod:      strings.TrimSpace(os.Getenv("POD_NAME")),
		BeruURL:        strings.TrimSpace(os.Getenv("BERU_HTTP_URL")),
		MemoryLimit:    int64(intFromEnv("SOLDIER_MEMORY_LIMIT", defaultMemoryLimit)),
		MaxConns:       intFromEnv("SOLDIER_MAX_CONNS", defaultMaxConns),
		QueueSize:      intFromEnv("SOLDIER_QUEUE_SIZE", beru.DefaultQueueSize),
		Workers:        intFromEnv("SOLDIER_WORKERS", beru.DefaultWorkers),
		TapChunks:      intFromEnv("SOLDIER_TAP_CHUNKS", defaultTapChunks),
	}

	var err error
	if cfg.DialTimeout, err = durationFromEnv("SOLDIER_DIAL_TIMEOUT", defaultDialTimeout); err != nil {
		return Config{}, err
	}
	if cfg.IdleTimeout, err = durationFromEnv("SOLDIER_IDLE_TIMEOUT", defaultIdleTimeout); err != nil {
		return Config{}, err
	}
	if cfg.HTTPTimeout, err = durationFromEnv("SOLDIER_HTTP_TIMEOUT", defaultHTTPTimeout); err != nil {
		return Config{}, err
	}

	raw := strings.TrimSpace(os.Getenv("SOLDIER_ROUTES"))
	if raw == "" {
		return Config{}, fmt.Errorf("SOLDIER_ROUTES is required")
	}
	if err := json.Unmarshal([]byte(raw), &cfg.Routes); err != nil {
		return Config{}, fmt.Errorf("SOLDIER_ROUTES: %w", err)
	}
	if len(cfg.Routes) == 0 {
		return Config{}, fmt.Errorf("SOLDIER_ROUTES: no routes configured")
	}

	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// Validate rejects a configuration that could only fail later, at a point where
// the failure would look like missing telemetry rather than a misconfiguration.
func (c Config) Validate() error {
	if _, ok := validRoles[c.Role]; !ok {
		return fmt.Errorf("SHADOW_ROLE must be control-a, control-b or candidate (got %q)", c.Role)
	}
	if c.BeruURL == "" {
		return fmt.Errorf("BERU_HTTP_URL is required")
	}
	seen := make(map[int]string, len(c.Routes))
	for _, r := range c.Routes {
		if err := r.Validate(); err != nil {
			return err
		}
		if prev, dup := seen[r.Listen]; dup {
			return fmt.Errorf("routes %s and %s both listen on port %d", prev, r.Protocol, r.Listen)
		}
		seen[r.Listen] = r.Protocol
	}
	return nil
}

func intFromEnv(key string, fallback int) int {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return fallback
	}
	return n
}

func durationFromEnv(key string, fallback time.Duration) (time.Duration, error) {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	return d, nil
}
