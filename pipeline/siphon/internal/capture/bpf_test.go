package capture

import (
	"strings"
	"testing"

	"github.com/shadow-diff/siphon/internal/config"
)

func TestBuildBPFFilter(t *testing.T) {
	tests := []struct {
		name        string
		cfg         config.SiphonConfig
		expectMatch []string
		expectError bool
	}{
		{
			name: "single IP and port with egress",
			cfg: config.SiphonConfig{
				Targets: []config.SiphonTarget{
					{
						TargetIPs:   []string{"10.0.0.1"},
						TargetPorts: []int{80},
					},
				},
			},
			expectMatch: []string{
				"tcp and ( (host 10.0.0.1 and port 80) or (src host 10.0.0.1) )",
			},
			expectError: false,
		},
		{
			name: "multiple target IPs and ports with sorting and dedup",
			cfg: config.SiphonConfig{
				Targets: []config.SiphonTarget{
					{
						TargetIPs:   []string{"10.0.0.2", "10.0.0.1"},
						TargetPorts: []int{80, 443},
					},
					{
						TargetIPs:   []string{"10.0.0.1"},
						TargetPorts: []int{80},
					},
				},
			},
			expectMatch: []string{
				"tcp and ( (host 10.0.0.1 and port 80) or (host 10.0.0.1 and port 443) or (host 10.0.0.2 and port 80) or (host 10.0.0.2 and port 443) or (src host 10.0.0.1) or (src host 10.0.0.2) )",
			},
			expectError: false,
		},
		{
			name: "IPv6 addresses are ignored",
			cfg: config.SiphonConfig{
				Targets: []config.SiphonTarget{
					{
						TargetIPs:   []string{"2001:db8::1", "10.0.0.1"},
						TargetPorts: []int{80},
					},
				},
			},
			expectMatch: []string{
				"tcp and ( (host 10.0.0.1 and port 80) or (src host 10.0.0.1) )",
			},
			expectError: false,
		},
		{
			name: "empty targets",
			cfg: config.SiphonConfig{
				Targets: []config.SiphonTarget{},
			},
			expectError: true,
		},
		{
			name: "invalid IPs or ports",
			cfg: config.SiphonConfig{
				Targets: []config.SiphonTarget{
					{
						TargetIPs:   []string{"not-an-ip"},
						TargetPorts: []int{80},
					},
				},
			},
			expectError: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := BuildBPFFilter(tc.cfg)
			if (err != nil) != tc.expectError {
				t.Fatalf("BuildBPFFilter() error = %v, expectError %v", err, tc.expectError)
			}
			if tc.expectError {
				return
			}
			for _, match := range tc.expectMatch {
				if got != match {
					t.Errorf("BuildBPFFilter() = %q, want %q", got, match)
				}
			}
		})
	}
}

func TestBuildBPFFilterWarningOnLongFilter(t *testing.T) {
	var targets []config.SiphonTarget
	for i := 0; i < 200; i++ {
		targets = append(targets, config.SiphonTarget{
			TargetIPs:   []string{"10.244.100.123", "10.244.100.124"},
			TargetPorts: []int{80, 443, 8080, 8443, 9000, 9001},
		})
	}
	cfg := config.SiphonConfig{
		Targets: targets,
	}

	filter, err := BuildBPFFilter(cfg)
	if err != nil {
		t.Fatalf("BuildBPFFilter() error = %v", err)
	}

	if len(filter) == 0 {
		t.Fatal("Expected non-empty filter")
	}

	if !strings.HasPrefix(filter, "tcp and ( ") {
		t.Errorf("Unexpected prefix: %q", filter[:15])
	}
}
