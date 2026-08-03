package controller

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	enginev1alpha1 "github.com/shadow-diff/monarch/api/v1alpha1"
)

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

	// Sidecars report on the ingest port, not 8080: the shadow pod's iptables
	// rules REDIRECT outbound 8080 into Envoy's egress listener.
	ingestURL := beruIngestURLFor(st, shadowNS)
	wantIngestURL := "http://beru-local.shadow-default-http-otel-rmq-nodejs-shadow.svc.cluster.local:8081"
	if ingestURL != wantIngestURL {
		t.Fatalf("beruIngestURLFor = %q, want %q", ingestURL, wantIngestURL)
	}

	recorderURL := "http://" + httpHost
	if strings.Contains(recorderURL, "http://http://") {
		t.Fatalf("double http prefix: %q", recorderURL)
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
	reason := podTerminalReason(pod, localBeruName)
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
	want := imageRegistryDefault + "/beru:dev"
	if got := beruImageFor(&enginev1alpha1.ShadowTest{}); got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestLocalBeruPodSelector(t *testing.T) {
	t.Parallel()
	sel := localBeruPodSelector()
	if !sel.Matches(labels.Set(map[string]string{"app": localBeruName})) {
		t.Fatal("selector should match beru-local app label")
	}
}
