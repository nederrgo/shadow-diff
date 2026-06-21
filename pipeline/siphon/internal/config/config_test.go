package config

import (
	"testing"
)

func TestConfigManager(t *testing.T) {
	mgr := NewManager()

	if mgr.HasAnyTargets() {
		t.Error("New manager should not have any targets")
	}

	cfg := SiphonConfig{
		SampleRate: 50,
		Targets: []SiphonTarget{
			{
				ShadowTest:  "default/my-st",
				TargetIPs:   []string{"10.244.1.2"},
				TargetPorts: []int{80, 8080},
				IgrisHost:   "my-igris-svc",
				Listeners: []SiphonListener{
					{Port: 80, Driver: "http_request"},
					{Port: 8080, Driver: "tcp_stream"},
				},
				RecordAndReplay: []SiphonRecordAndReplayHost{
					{Host: "httpbin.org"},
				},
			},
		},
	}

	mgr.Update(cfg)

	if !mgr.HasAnyTargets() {
		t.Error("Expected manager to have targets after update")
	}

	if !mgr.IsTarget("10.244.1.2", 80) {
		t.Error("Expected 10.244.1.2:80 to be a target")
	}

	if mgr.IsTarget("10.244.1.2", 90) {
		t.Error("Did not expect 10.244.1.2:90 to be a target")
	}

	target, driver, ok := mgr.LookupTarget("10.244.1.2", 80)
	if !ok {
		t.Fatal("Failed to lookup target 10.244.1.2:80")
	}
	if target.ShadowTest != "default/my-st" {
		t.Errorf("Unexpected ShadowTest name: %s", target.ShadowTest)
	}
	if driver != "http_request" {
		t.Errorf("Unexpected driver: %s", driver)
	}

	_, driverStream, ok := mgr.LookupTarget("10.244.1.2", 8080)
	if !ok || driverStream != "tcp_stream" {
		t.Errorf("Expected driver stream to be tcp_stream, got %s (ok=%v)", driverStream, ok)
	}

	if !mgr.IsProdPodIP("10.244.1.2") {
		t.Error("Expected prod pod IP")
	}
	if !mgr.ShouldRecordEgress("10.244.1.2", "93.184.216.34", 80, "") {
		t.Error("Expected egress outbound to be recordable")
	}
	if mgr.ShouldRecordEgress("10.244.1.2", "10.244.1.2", 80, "") {
		t.Error("Did not expect ingress destination to be recorded as egress")
	}
}
