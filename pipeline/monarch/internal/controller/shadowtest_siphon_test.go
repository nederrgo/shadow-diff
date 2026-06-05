package controller

import (
	"testing"

	corev1 "k8s.io/api/core/v1"

	enginev1alpha1 "github.com/shadow-diff/monarch/api/v1alpha1"
)

func TestSiphonAgentAPIHost(t *testing.T) {
	pod := corev1.Pod{}
	pod.Spec.HostNetwork = true
	pod.Status.HostIP = "172.18.0.2"
	pod.Status.PodIP = "10.244.0.5"
	if got := siphonAgentAPIHost(pod); got != "172.18.0.2" {
		t.Fatalf("hostNetwork: got %q want 172.18.0.2", got)
	}
	pod.Spec.HostNetwork = false
	if got := siphonAgentAPIHost(pod); got != "10.244.0.5" {
		t.Fatalf("pod network: got %q want 10.244.0.5", got)
	}
}

func TestBuildSiphonTarget(t *testing.T) {
	st := &enginev1alpha1.ShadowTest{}
	st.Namespace = "default"
	st.Name = "my-st"
	st.Spec.ServicePort = 8080

	st.Spec.Downstreams = []enginev1alpha1.DownstreamSpec{{Host: "httpbin.org"}}

	target := buildSiphonTarget(st, "shadow-default-my-st", []string{"10.244.1.2"}, nil)
	if target.ShadowTest != "default/my-st" {
		t.Fatalf("shadowtest id %q", target.ShadowTest)
	}
	if len(target.TargetIPs) != 1 || target.TargetIPs[0] != "10.244.1.2" {
		t.Fatalf("ips %v", target.TargetIPs)
	}
	if target.IgrisHost == "" {
		t.Fatal("igris host empty")
	}
	if len(target.Listeners) == 0 {
		t.Fatal("expected default listener")
	}
	wantRecorder := "my-st-recorder.shadow-default-my-st.svc.cluster.local:8080"
	if target.RecorderHost != wantRecorder {
		t.Fatalf("recorder host %q want %q", target.RecorderHost, wantRecorder)
	}
	if len(target.Downstreams) != 1 || target.Downstreams[0].Host != "httpbin.org" {
		t.Fatalf("downstreams %v", target.Downstreams)
	}
}
