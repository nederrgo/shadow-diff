package controller

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	enginev1alpha1 "github.com/shadow-diff/monarch/api/v1alpha1"
)

func TestUsesLocalBeru(t *testing.T) {
	t.Parallel()
	if !usesLocalBeru(&enginev1alpha1.ShadowTest{}) {
		t.Fatal("expected local Beru when spec.beruGRPCAddress empty")
	}
	st := &enginev1alpha1.ShadowTest{
		Spec: enginev1alpha1.ShadowTestSpec{
			BeruGRPCAddress: "beru.beru-system.svc.cluster.local:50051",
		},
	}
	if usesLocalBeru(st) {
		t.Fatal("expected external Beru when spec override set")
	}
}

func TestLocalBeruAddressHelpers(t *testing.T) {
	t.Parallel()
	const shadowNS = "shadow-default-http-otel-rmq-nodejs-shadow"
	st := &enginev1alpha1.ShadowTest{
		ObjectMeta: metav1.ObjectMeta{Name: "http-otel-rmq-nodejs-shadow", Namespace: "default"},
	}

	grpc := beruGRPCAddressFor(st, shadowNS)
	if strings.HasPrefix(grpc, "http://") {
		t.Fatalf("beruGRPCAddressFor must be bare host:port, got %q", grpc)
	}
	wantGRPC := "beru-local.shadow-default-http-otel-rmq-nodejs-shadow.svc.cluster.local:50051"
	if grpc != wantGRPC {
		t.Fatalf("beruGRPCAddressFor = %q, want %q", grpc, wantGRPC)
	}

	httpHost := beruHTTPHostFor(st, shadowNS)
	if strings.HasPrefix(httpHost, "http://") {
		t.Fatalf("beruHTTPHostFor must be bare host:port, got %q", httpHost)
	}
	wantHTTP := "beru-local.shadow-default-http-otel-rmq-nodejs-shadow.svc.cluster.local:8080"
	if httpHost != wantHTTP {
		t.Fatalf("beruHTTPHostFor = %q, want %q", httpHost, wantHTTP)
	}

	otlp := beruOTLPEndpointFor(st, shadowNS)
	wantOTLP := "http://beru-local.shadow-default-http-otel-rmq-nodejs-shadow.svc.cluster.local:4317"
	if otlp != wantOTLP {
		t.Fatalf("beruOTLPEndpointFor = %q, want %q", otlp, wantOTLP)
	}

	otlpHTTP := beruOTLPHTTPEndpointFor(st, shadowNS)
	wantOTLPHTTP := "http://beru-local.shadow-default-http-otel-rmq-nodejs-shadow.svc.cluster.local:8080"
	if otlpHTTP != wantOTLPHTTP {
		t.Fatalf("beruOTLPHTTPEndpointFor = %q, want %q", otlpHTTP, wantOTLPHTTP)
	}

	recorderURL := "http://" + httpHost
	if strings.Contains(recorderURL, "http://http://") {
		t.Fatalf("double http prefix: %q", recorderURL)
	}
}

func TestLocalBeruAddressHelpers_externalOverride(t *testing.T) {
	t.Parallel()
	st := &enginev1alpha1.ShadowTest{
		Spec: enginev1alpha1.ShadowTestSpec{
			BeruGRPCAddress: "beru.beru-system.svc.cluster.local:50051",
		},
	}
	if got := beruGRPCAddressFor(st, "shadow-default-x"); got != st.Spec.BeruGRPCAddress {
		t.Fatalf("override grpc = %q", got)
	}
	if got := beruHTTPHostFor(st, "shadow-default-x"); got != "beru.beru-system.svc.cluster.local:8080" {
		t.Fatalf("override http host = %q", got)
	}
	if got := beruOTLPEndpointFor(st, "shadow-default-x"); got != defaultBeruOTLPEndpoint {
		t.Fatalf("override otlp = %q", got)
	}
}

func TestPodTerminalReason_imagePullBackOff(t *testing.T) {
	t.Parallel()
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "beru-local-abc"},
		Status: corev1.PodStatus{
			ContainerStatuses: []corev1.ContainerStatus{{
				Name: "beru",
				State: corev1.ContainerState{
					Waiting: &corev1.ContainerStateWaiting{
						Reason:  "ImagePullBackOff",
						Message: "Back-off pulling image beru:dev",
					},
				},
			}},
		},
	}
	reason := podTerminalReason(pod)
	if !reason.terminal {
		t.Fatal("expected terminal reason for ImagePullBackOff")
	}
	if !strings.Contains(reason.message, "ImagePullBackOff") {
		t.Fatalf("message = %q", reason.message)
	}
}

func TestBeruImageFor(t *testing.T) {
	t.Setenv("MONARCH_MODE", "dev")
	t.Setenv(envBeruImage, "")
	if got := beruImageFor(&enginev1alpha1.ShadowTest{}); got != "beru:dev" {
		t.Fatalf("got %q want beru:dev", got)
	}
}

func TestLocalBeruPodSelector(t *testing.T) {
	t.Parallel()
	sel := localBeruPodSelector()
	if !sel.Matches(labels.Set(map[string]string{"app": localBeruName})) {
		t.Fatal("selector should match beru-local app label")
	}
}
