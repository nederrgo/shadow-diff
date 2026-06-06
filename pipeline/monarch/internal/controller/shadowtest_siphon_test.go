package controller

import (
	"testing"

	appsv1 "k8s.io/api/apps/v1"
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

func boolPtr(v bool) *bool { return &v }

func TestSiphonEnabled(t *testing.T) {
	dep := &appsv1.Deployment{
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Ports: []corev1.ContainerPort{{ContainerPort: 80}},
					}},
				},
			},
		},
	}

	st := &enginev1alpha1.ShadowTest{}
	if siphonEnabled(st, dep) {
		t.Fatal("expected disabled with no inputs, downstreams, or explicit enable")
	}

	st.Spec.Siphon = &enginev1alpha1.SiphonSpec{Enabled: boolPtr(false)}
	if siphonEnabled(st, dep) {
		t.Fatal("explicit false should disable")
	}

	st.Spec.Siphon = &enginev1alpha1.SiphonSpec{Enabled: boolPtr(true)}
	if !siphonEnabled(st, dep) {
		t.Fatal("explicit true should enable")
	}

	st = &enginev1alpha1.ShadowTest{
		Spec: enginev1alpha1.ShadowTestSpec{
			Downstreams: []enginev1alpha1.DownstreamSpec{{Host: "example.com"}},
		},
	}
	if !siphonEnabled(st, dep) {
		t.Fatal("downstreams should enable siphon")
	}

	st = &enginev1alpha1.ShadowTest{
		Spec: enginev1alpha1.ShadowTestSpec{
			Inputs: []enginev1alpha1.InputSpec{{Port: 80, Driver: "http_request"}},
		},
	}
	if !siphonEnabled(st, dep) {
		t.Fatal("matching ingress port should enable siphon")
	}

	st.Spec.Inputs = []enginev1alpha1.InputSpec{{Port: 9999, Driver: "http_request"}}
	if siphonEnabled(st, dep) {
		t.Fatal("non-matching port should not enable siphon")
	}

	st = &enginev1alpha1.ShadowTest{
		Spec: enginev1alpha1.ShadowTestSpec{
			Downstreams: []enginev1alpha1.DownstreamSpec{{Host: "example.com"}},
			Siphon:      &enginev1alpha1.SiphonSpec{Enabled: boolPtr(false)},
		},
	}
	if siphonEnabled(st, dep) {
		t.Fatal("explicit false should override downstreams")
	}
}
