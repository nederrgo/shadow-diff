package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func validCfg() Config {
	return Config{
		ControlAURL:    "http://a:8080",
		ControlBURL:    "https://b:8443",
		CandidateURL:   "http://c:8080",
		ControlAAddr:   "a.shadow.svc.cluster.local",
		ControlBAddr:   "b.shadow.svc.cluster.local",
		CandidateAddr:  "c.shadow.svc.cluster.local",
		Listeners:      []Listener{{Port: 80, Driver: "http_request"}},
		MaxTCPConns:    1024,
		TCPDialTimeout: defaultTCPDialTimeout,
		TCPIdleTimeout: defaultTCPIdleTimeout,
	}
}

func TestValidate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		cfg     Config
		wantErr bool
	}{
		{name: "valid", cfg: validCfg()},
		{
			name: "missing url",
			cfg: func() Config {
				c := validCfg()
				c.ControlAURL = ""
				return c
			}(),
			wantErr: true,
		},
		{
			name: "missing addr",
			cfg: func() Config {
				c := validCfg()
				c.ControlAAddr = ""
				return c
			}(),
			wantErr: true,
		},
		{
			name: "addr with port",
			cfg: func() Config {
				c := validCfg()
				c.ControlAAddr = "host:27017"
				return c
			}(),
			wantErr: true,
		},
		{
			name: "bad scheme",
			cfg: func() Config {
				c := validCfg()
				c.ControlAURL = "ftp://a"
				return c
			}(),
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.cfg.Validate()
			if (err != nil) != tt.wantErr {
				t.Fatalf("Validate() err=%v wantErr=%v", err, tt.wantErr)
			}
		})
	}
}

func TestLoadListenersFromFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "listeners.json")
	data, _ := json.Marshal([]listenerFileEntry{
		{Port: 80, Driver: "http_request"},
		{Port: 9090, Driver: "tcp_stream"},
	})
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	listeners, err := loadListeners(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(listeners) != 2 || listeners[0].Driver != "http_request" {
		t.Fatalf("got %+v", listeners)
	}
}

func TestLoadListenersLegacyAddon(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "listeners.json")
	data := []byte(`[{"port":80,"addon":"http"}]`)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	listeners, err := loadListeners(path)
	if err != nil {
		t.Fatal(err)
	}
	if listeners[0].Driver != "http_request" {
		t.Fatalf("got %+v", listeners)
	}
}

func TestLoadListenersMissingDefaults(t *testing.T) {
	t.Parallel()
	listeners, err := loadListeners(filepath.Join(t.TempDir(), "missing.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(listeners) != 1 || listeners[0].Port != 8080 || listeners[0].Driver != "http_request" {
		t.Fatalf("got %+v", listeners)
	}
}

func TestTargetAddrsForPort(t *testing.T) {
	t.Parallel()
	cfg := validCfg()
	addrs := cfg.TargetAddrsForPort(27017)
	if len(addrs) != 3 {
		t.Fatal(addrs)
	}
	if addrs[0] != "a.shadow.svc.cluster.local:27017" {
		t.Fatalf("got %q", addrs[0])
	}
}

func TestNormalizeDriver(t *testing.T) {
	t.Parallel()
	if got := normalizeDriver("http", ""); got != "http_request" {
		t.Fatalf("got %q", got)
	}
	if got := normalizeDriver("", "http"); got != "http_request" {
		t.Fatalf("got %q", got)
	}
	if got := normalizeDriver("tcp_stream", ""); got != "tcp_stream" {
		t.Fatalf("got %q", got)
	}
}
