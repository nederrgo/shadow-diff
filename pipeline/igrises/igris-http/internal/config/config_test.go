package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func validCfg() Config {
	return Config{
		OperatingMode:  "replay",
		ControlAURL:    "http://a:8080",
		ControlBURL:    "https://b:8443",
		CandidateURL:   "http://c:8080",
		ControlAAddr:   "a.shadow.svc.cluster.local",
		ControlBAddr:   "b.shadow.svc.cluster.local",
		CandidateAddr:  "c.shadow.svc.cluster.local",
		Listeners:      []Listener{{Port: 80, Driver: "http_request"}},
		MaxTCPConns:    1024,
		MaxBodySize:    defaultMaxBodySize,
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
			name: "record mode without targets",
			cfg: func() Config {
				c := validCfg()
				c.OperatingMode = "record"
				c.ControlAURL = ""
				c.ControlBURL = ""
				c.CandidateURL = ""
				c.ControlAAddr = ""
				c.ControlBAddr = ""
				c.CandidateAddr = ""
				return c
			}(),
		},
		{
			name: "replay mode without addrs",
			cfg: func() Config {
				c := validCfg()
				c.OperatingMode = "replay"
				c.ControlAAddr = ""
				c.ControlBAddr = ""
				c.CandidateAddr = ""
				return c
			}(),
		},
		{
			name: "replay mode missing url",
			cfg: func() Config {
				c := validCfg()
				c.OperatingMode = "replay"
				c.ControlAURL = ""
				c.ControlAAddr = ""
				c.ControlBAddr = ""
				c.CandidateAddr = ""
				return c
			}(),
			wantErr: true,
		},
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
			name: "replay ignores missing addr",
			cfg: func() Config {
				c := validCfg()
				c.ControlAAddr = ""
				return c
			}(),
		},
		{
			name: "invalid operating mode",
			cfg: func() Config {
				c := validCfg()
				c.OperatingMode = "live"
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
		{
			name: "tcp_stream driver rejected",
			cfg: func() Config {
				c := validCfg()
				c.Listeners = []Listener{{Port: 9090, Driver: "tcp_stream"}}
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
		{Port: 8080, Driver: "http_request"},
	})
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	listeners, err := loadListeners(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(listeners) != 2 || listeners[0].Driver != "http_request" || listeners[1].Driver != "http_request" {
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

func TestIntFromEnvSigned(t *testing.T) {
	const key = "IGRIS_TEST_MAX_CONCURRENCY"
	tests := []struct {
		name string
		val  string
		def  int
		want int
	}{
		{name: "unset", val: "", def: 50, want: 50},
		{name: "zero falls back to default", val: "0", def: 50, want: 50},
		{name: "negative means unlimited", val: "-1", def: 50, want: -1},
		{name: "positive passthrough", val: "10", def: 50, want: 10},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.val == "" {
				os.Unsetenv(key)
			} else {
				t.Setenv(key, tt.val)
			}
			if got := intFromEnvSigned(key, tt.def); got != tt.want {
				t.Fatalf("intFromEnvSigned(%q, %d) = %d want %d", tt.val, tt.def, got, tt.want)
			}
		})
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
		// Unknown drivers are left as-is so Validate can reject them.
		t.Fatalf("got %q", got)
	}
}

func TestFirstEnvPrefersShadowURL(t *testing.T) {
	t.Setenv("SHADOW_CONTROL_A_URL", "http://shadow-a:8888")
	t.Setenv("CONTROL_A_URL", "http://control-a:8888")
	if got := firstEnv("SHADOW_CONTROL_A_URL", "CONTROL_A_URL"); got != "http://shadow-a:8888" {
		t.Fatalf("got %q", got)
	}
	t.Setenv("SHADOW_CONTROL_A_URL", "")
	if got := firstEnv("SHADOW_CONTROL_A_URL", "CONTROL_A_URL"); got != "http://control-a:8888" {
		t.Fatalf("fallback got %q", got)
	}
}
