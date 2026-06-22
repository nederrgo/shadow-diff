package controller

import (
	"encoding/json"
	"testing"
	"strings"
)

func TestBuildNetObservFlowFilterRules_emptyFallback(t *testing.T) {
	got, err := buildNetObservFlowFilterRules(nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != netObservFlowFilterFallback {
		t.Fatalf("got %q want %q", got, netObservFlowFilterFallback)
	}
}

func TestBuildNetObservFlowFilterRules_noDoubleEscape(t *testing.T) {
	got, err := buildNetObservFlowFilterRules([]string{"10.244.1.2", "10.244.1.3"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(got, `"`) {
		t.Fatalf("JSON must not be wrapped in extra quotes: %q", got)
	}
	var rules []flowFilterRule
	if err := json.Unmarshal([]byte(got), &rules); err != nil {
		t.Fatalf("invalid JSON: %v (%q)", err, got)
	}
	if len(rules) != 2 {
		t.Fatalf("rules = %+v", rules)
	}
}

func TestNetObservContainer_privilegedAndPCAEnv(t *testing.T) {
	c := netObservContainer(defaultNetObservImage)
	if c.SecurityContext == nil || c.SecurityContext.Privileged == nil || !*c.SecurityContext.Privileged {
		t.Fatal("netobserv container must be privileged")
	}
	if c.SecurityContext.RunAsUser == nil || *c.SecurityContext.RunAsUser != 0 {
		t.Fatal("netobserv container must runAsUser 0 (Kind eBPF memlock)")
	}
	env := map[string]string{}
	for _, e := range c.Env {
		env[e.Name] = e.Value
	}
	if env[envEnablePCA] != "true" {
		t.Fatalf("ENABLE_PCA = %q", env[envEnablePCA])
	}
	if env[envTargetHost] != netObservTargetHost {
		t.Fatalf("TARGET_HOST = %q", env[envTargetHost])
	}
	if env[envTargetPort] != "9990" {
		t.Fatalf("TARGET_PORT = %q", env[envTargetPort])
	}
	if env[envFlowFilterRules] != netObservFlowFilterFallback {
		t.Fatalf("FLOW_FILTER_RULES = %q", env[envFlowFilterRules])
	}
	if env[envCacheActiveTimeout] != "5s" {
		t.Fatalf("CACHE_ACTIVE_TIMEOUT = %q", env[envCacheActiveTimeout])
	}
	if env[envCacheMaxFlows] != "50000" {
		t.Fatalf("CACHE_MAX_FLOWS = %q", env[envCacheMaxFlows])
	}
	if env[envDeduper] != "firstCome" {
		t.Fatalf("DEDUPER = %q", env[envDeduper])
	}
	if env[envExcludeInterfaces] != "" {
		t.Fatalf("EXCLUDE_INTERFACES = %q want empty", env[envExcludeInterfaces])
	}
	if env[envTCAttachMode] != "tcx" {
		t.Fatalf("TC_ATTACH_MODE = %q", env[envTCAttachMode])
	}
	if env[envTCAttachRetries] != "10" {
		t.Fatalf("TC_ATTACH_RETRIES = %q", env[envTCAttachRetries])
	}
	if len(c.VolumeMounts) != 2 || c.VolumeMounts[1].MountPath != "/var/run/siphon" {
		t.Fatalf("VolumeMounts = %+v", c.VolumeMounts)
	}
	if c.Command == nil || c.Command[0] != "/bin/sh" {
		t.Fatalf("Command = %+v", c.Command)
	}
	vols := netObservPodVolumes()
	if len(vols) != 2 || vols[1].EmptyDir == nil {
		t.Fatalf("netObservPodVolumes = %+v", vols)
	}
}

func TestNetObservStableFlowFilterRules_matchesFallback(t *testing.T) {
	if netObservStableFlowFilterRules() != `[{"ip_cidr":"0.0.0.0/0","protocol":"TCP","action":"Accept"}]` {
		t.Fatal("stable rules must use exact fallback string")
	}
}
