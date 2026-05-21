package config

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"runtime"
	"strings"
	"time"
)

const (
	defaultListenersFile   = "/etc/igris/listeners.json"
	defaultMaxTCPConns     = 1024
	defaultTCPDialTimeout  = 5 * time.Second
	defaultTCPIdleTimeout  = 5 * time.Minute
)

// Listener binds a port to an input driver.
type Listener struct {
	Port   int    `json:"port"`
	Driver string `json:"driver"`
	Addon  string `json:"addon,omitempty"` // deprecated alias
}

// TargetHost is a named shadow host (port appended per listener for TCP).
type TargetHost struct {
	Name string
	Host string
}

// Config holds Igris process configuration.
type Config struct {
	Listeners        []Listener
	ControlAURL      string
	ControlBURL      string
	CandidateURL     string
	ControlAAddr     string
	ControlBAddr     string
	CandidateAddr    string
	WorkerPoolSize   int
	MaxTCPConns      int
	TCPDialTimeout   time.Duration
	TCPIdleTimeout   time.Duration
}

// Load reads configuration from the environment, validates it, and exits on failure.
func Load() Config {
	cfg := Config{
		ControlAURL:    os.Getenv("CONTROL_A_URL"),
		ControlBURL:    os.Getenv("CONTROL_B_URL"),
		CandidateURL:   os.Getenv("CANDIDATE_URL"),
		ControlAAddr:   os.Getenv("CONTROL_A_ADDR"),
		ControlBAddr:   os.Getenv("CONTROL_B_ADDR"),
		CandidateAddr:  os.Getenv("CANDIDATE_ADDR"),
		MaxTCPConns:    defaultMaxTCPConns,
		TCPDialTimeout: defaultTCPDialTimeout,
		TCPIdleTimeout: defaultTCPIdleTimeout,
	}
	cfg.WorkerPoolSize = workerPoolSizeFromEnv()
	cfg.MaxTCPConns = intFromEnv("IGRIS_MAX_TCP_CONNS", defaultMaxTCPConns)
	if d, ok := durationFromEnv("IGRIS_TCP_DIAL_TIMEOUT"); ok {
		cfg.TCPDialTimeout = d
	}
	if d, ok := durationFromEnv("IGRIS_TCP_IDLE_TIMEOUT"); ok {
		cfg.TCPIdleTimeout = d
	}

	listenersFile := os.Getenv("IGRIS_LISTENERS_FILE")
	if listenersFile == "" {
		listenersFile = defaultListenersFile
	}
	listeners, err := loadListeners(listenersFile)
	if err != nil {
		slog.Error("invalid listeners configuration", "err", err, "file", listenersFile)
		os.Exit(1)
	}
	cfg.Listeners = listeners

	if err := cfg.Validate(); err != nil {
		slog.Error("invalid configuration", "err", err)
		os.Exit(1)
	}
	return cfg
}

func workerPoolSizeFromEnv() int {
	const defaultWorkers = 32
	if v := os.Getenv("IGRIS_WORKER_POOL_SIZE"); v != "" {
		var n int
		if _, err := fmt.Sscanf(v, "%d", &n); err == nil && n > 0 {
			return n
		}
	}
	n := runtime.NumCPU() * 4
	if n > defaultWorkers {
		return defaultWorkers
	}
	if n < 1 {
		return 1
	}
	return n
}

func intFromEnv(key string, def int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	var n int
	if _, err := fmt.Sscanf(v, "%d", &n); err == nil && n > 0 {
		return n
	}
	return def
}

func durationFromEnv(key string) (time.Duration, bool) {
	v := os.Getenv(key)
	if v == "" {
		return 0, false
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		return 0, false
	}
	return d, true
}

type listenerFileEntry struct {
	Port   int    `json:"port"`
	Driver string `json:"driver"`
	Addon  string `json:"addon,omitempty"`
}

func loadListeners(path string) ([]Listener, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return []Listener{{Port: 8080, Driver: "http_request"}}, nil
		}
		return nil, err
	}
	var raw []listenerFileEntry
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	if len(raw) == 0 {
		return nil, fmt.Errorf("listeners file %s is empty", path)
	}
	out := make([]Listener, len(raw))
	for i, e := range raw {
		d := normalizeDriver(e.Driver, e.Addon)
		if d == "" {
			return nil, fmt.Errorf("listener on port %d missing driver", e.Port)
		}
		out[i] = Listener{Port: e.Port, Driver: d}
	}
	return out, nil
}

func normalizeDriver(driver, addon string) string {
	d := strings.TrimSpace(strings.ToLower(driver))
	if d == "" {
		d = strings.TrimSpace(strings.ToLower(addon))
	}
	switch d {
	case "", "http", "http_request":
		return "http_request"
	case "tcp_stream":
		return "tcp_stream"
	default:
		return d
	}
}

// Validate checks target URLs, hosts, and listener definitions.
func (c Config) Validate() error {
	targets := []struct {
		name string
		raw  string
	}{
		{"CONTROL_A_URL", c.ControlAURL},
		{"CONTROL_B_URL", c.ControlBURL},
		{"CANDIDATE_URL", c.CandidateURL},
	}
	for _, t := range targets {
		if err := validateTargetURL(t.name, t.raw); err != nil {
			return err
		}
	}
	addrs := []struct {
		name string
		raw  string
	}{
		{"CONTROL_A_ADDR", c.ControlAAddr},
		{"CONTROL_B_ADDR", c.ControlBAddr},
		{"CANDIDATE_ADDR", c.CandidateAddr},
	}
	for _, t := range addrs {
		if err := validateTargetHost(t.name, t.raw); err != nil {
			return err
		}
	}
	for _, l := range c.Listeners {
		if l.Port < 1 || l.Port > 65535 {
			return fmt.Errorf("listener port %d out of range", l.Port)
		}
		switch l.Driver {
		case "http_request", "tcp_stream":
		default:
			return fmt.Errorf("unknown driver %q for port %d", l.Driver, l.Port)
		}
	}
	if c.MaxTCPConns < 1 {
		return fmt.Errorf("IGRIS_MAX_TCP_CONNS must be positive")
	}
	if c.TCPDialTimeout <= 0 || c.TCPIdleTimeout <= 0 {
		return fmt.Errorf("TCP timeouts must be positive")
	}
	return nil
}

func validateTargetURL(name, raw string) error {
	if raw == "" {
		return fmt.Errorf("%s is required", name)
	}
	u, err := url.ParseRequestURI(raw)
	if err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return fmt.Errorf("%s: scheme must be http or https, got %q", name, u.Scheme)
	}
	return nil
}

func validateTargetHost(name, raw string) error {
	if raw == "" {
		return fmt.Errorf("%s is required", name)
	}
	if strings.Contains(raw, "://") {
		return fmt.Errorf("%s: must be host only, not a URL", name)
	}
	// Reject host:port in ADDR; port is appended per listener.
	if h, _, err := splitHostPortOptional(raw); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	} else if h != raw {
		return fmt.Errorf("%s: must be host only (no port); got %q", name, raw)
	}
	return nil
}

func splitHostPortOptional(hostport string) (host, port string, err error) {
	if !strings.Contains(hostport, ":") {
		return hostport, "", nil
	}
	// Bracketed IPv6
	if strings.HasPrefix(hostport, "[") {
		idx := strings.LastIndex(hostport, "]:")
		if idx < 0 {
			return hostport, "", nil
		}
		return hostport[:idx+1], hostport[idx+2:], nil
	}
	host, port, _ = strings.Cut(hostport, ":")
	return host, port, nil
}

// Targets returns HTTP multicast destinations in stable order.
func (c Config) Targets() []TargetURL {
	return []TargetURL{
		{Name: "control-a", BaseURL: c.ControlAURL},
		{Name: "control-b", BaseURL: c.ControlBURL},
		{Name: "candidate", BaseURL: c.CandidateURL},
	}
}

// TargetHosts returns TCP multicast host bases in stable order.
func (c Config) TargetHosts() []TargetHost {
	return []TargetHost{
		{Name: "control-a", Host: c.ControlAAddr},
		{Name: "control-b", Host: c.ControlBAddr},
		{Name: "candidate", Host: c.CandidateAddr},
	}
}

// TargetAddrsForPort builds host:port dial addresses for a listener port.
func (c Config) TargetAddrsForPort(port int) []string {
	hosts := c.TargetHosts()
	out := make([]string, len(hosts))
	for i, h := range hosts {
		out[i] = fmt.Sprintf("%s:%d", h.Host, port)
	}
	return out
}

// TargetURL names an HTTP multicast destination.
type TargetURL struct {
	Name    string
	BaseURL string
}
