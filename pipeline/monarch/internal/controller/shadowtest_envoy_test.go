package controller

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	enginev1alpha1 "github.com/shadow-diff/monarch/api/v1alpha1"
)

func TestRenderEnvoyYAML(t *testing.T) {
	st := &enginev1alpha1.ShadowTest{
		ObjectMeta: metav1.ObjectMeta{Name: "test"},
		Spec: enginev1alpha1.ShadowTestSpec{
			ServicePort:     80,
			ApplicationPort: 8080,
			BeruGRPCAddress: "beru.beru-system.svc.cluster.local:50051",
			BeruGRPCTimeout: "2s",
		},
	}
	yaml, err := renderEnvoyYAML(st, "shadow-default-test", roleControlA)
	if err != nil {
		t.Fatal(err)
	}
	checks := []string{
		"traceparent is not mutated",
		"generate_request_id: true",
		"x-shadow-trace-id",
		"ADD_IF_ABSENT",
		"x-shadow-role",
		"value: \"control-a\"",
		"initial_metadata",
		"envoy.extensions.filters.http.ext_proc.v3.ExternalProcessor",
		"envoy.filters.http.ext_proc",
		"envoy.filters.http.header_mutation",
		"beru_ext_proc",
		"cluster: local_app",
		"port_value: 80",
		"port_value: 8080",
		"failure_mode_allow: true",
		"response_body_mode: BUFFERED",
		"egress_stub",
	}
	for _, c := range checks {
		if !strings.Contains(yaml, c) {
			t.Fatalf("expected %q in envoy yaml:\n%s", c, yaml)
		}
	}
}

func TestRenderEnvoyYAML_egressProxy(t *testing.T) {
	st := &enginev1alpha1.ShadowTest{
		ObjectMeta: metav1.ObjectMeta{Name: "test"},
		Spec: enginev1alpha1.ShadowTestSpec{
			ServicePort:     80,
			ApplicationPort: 8080,
			BeruGRPCAddress: "beru.beru-system.svc.cluster.local:50051",
			BeruGRPCTimeout: "2s",
			Downstreams: []enginev1alpha1.DownstreamSpec{
				{Host: "api.stripe.com", IgnoreRequestPaths: []string{"$.timestamp"}},
			},
		},
	}
	yaml, err := renderEnvoyYAML(st, "shadow-default-test", roleControlA)
	if err != nil {
		t.Fatal(err)
	}
	checks := []string{
		"traceparent pass-through on egress",
		"name: egress_proxy",
		"port_value: 15001",
		"x-shadow-mode",
		"value: \"egress\"",
		"x-shadow-downstreams-config",
		"api.stripe.com",
		"api.stripe.com:*",
		"request_body_mode: BUFFERED",
		"response_body_mode: NONE",
		"failure_mode_allow: false",
		"egress_blackhole",
	}
	for _, c := range checks {
		if !strings.Contains(yaml, c) {
			t.Fatalf("expected %q in envoy yaml:\n%s", c, yaml)
		}
	}
	if strings.Contains(yaml, "egress_stub") {
		t.Fatal("expected egress_proxy, not egress_stub")
	}
	if strings.Contains(yaml, "response_body_mode: SKIP") {
		t.Fatal("Envoy BodySendMode does not support SKIP; use NONE for response_body_mode")
	}
}

func TestEgressVirtualHostDomains(t *testing.T) {
	got := egressVirtualHostDomains([]string{"api.stripe.com", "api.stripe.com", "payments.internal.svc"})
	want := []string{"api.stripe.com", "api.stripe.com:*", "payments.internal.svc", "payments.internal.svc:*"}
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v want %v", got, want)
		}
	}
	if len(egressVirtualHostDomains(nil)) != 0 {
		t.Fatal("expected empty domains for empty hosts")
	}
}

func TestApplicationPortFor_defaultOffset(t *testing.T) {
	st := &enginev1alpha1.ShadowTest{
		Spec: enginev1alpha1.ShadowTestSpec{ServicePort: 80},
	}
	if got := applicationPortFor(st); got != 8080 {
		t.Fatalf("expected 8080, got %d", got)
	}
}

func TestServicePortFor_default8888(t *testing.T) {
	st := &enginev1alpha1.ShadowTest{}
	if got := servicePortFor(st); got != 8888 {
		t.Fatalf("expected 8888, got %d", got)
	}
	st.Spec.ServicePort = 3000
	if got := servicePortFor(st); got != 3000 {
		t.Fatalf("expected 3000, got %d", got)
	}
}

func TestAppEnvWithEgressProxy(t *testing.T) {
	st := &enginev1alpha1.ShadowTest{
		Spec: enginev1alpha1.ShadowTestSpec{
			Downstreams: []enginev1alpha1.DownstreamSpec{{Host: "api.example.com"}},
		},
	}
	env := appEnvWithEgressProxy(st, []corev1.EnvVar{{Name: "FOO", Value: "bar"}})
	if len(env) != 4 {
		t.Fatalf("expected 4 env vars, got %d", len(env))
	}
	found := map[string]string{}
	for _, e := range env {
		found[e.Name] = e.Value
	}
	if found[envHTTPProxy] != egressProxyURL {
		t.Fatalf("HTTP_PROXY = %q", found[envHTTPProxy])
	}
	if found[envHTTPSProxy] != egressProxyURL {
		t.Fatalf("HTTPS_PROXY = %q", found[envHTTPSProxy])
	}
	if found[envNoProxy] != defaultNoProxyValue {
		t.Fatalf("NO_PROXY = %q", found[envNoProxy])
	}

	empty := appEnvWithEgressProxy(&enginev1alpha1.ShadowTest{}, []corev1.EnvVar{{Name: "FOO", Value: "bar"}})
	if len(empty) != 1 {
		t.Fatalf("expected no proxy env without downstreams, got %d", len(empty))
	}
}

func TestRenderEnvoyYAML_mongoEgress(t *testing.T) {
	st := &enginev1alpha1.ShadowTest{
		ObjectMeta: metav1.ObjectMeta{Name: "test"},
		Spec: enginev1alpha1.ShadowTestSpec{
			ServicePort:     8888,
			ApplicationPort: 8080,
			BeruGRPCAddress: "beru.beru-system.svc.cluster.local:50051",
			BeruGRPCTimeout: "2s",
			Dependencies: []enginev1alpha1.DependencySpec{{
				Name: "mongo", Image: "mongo:7", Port: 27017, EnvVarInjection: "MONGO_URL",
			}},
		},
	}
	yaml, err := renderEnvoyYAML(st, "shadow-default-test", roleControlA)
	if err != nil {
		t.Fatal(err)
	}
	checks := []string{
		"name: mongo_egress",
		"envoy.filters.network.mongo_proxy",
		"emit_dynamic_metadata: true",
		"port_value: 27017",
		"envoy.access_loggers.tcp_grpc",
		"envoy.extensions.access_loggers.grpc.v3.TcpGrpcAccessLogConfig",
		"transport_api_version: V3",
		"cluster_name: beru_als",
		"name: beru_als",
		"mongo_upstream",
		"log_name: mongo_egress_control-a",
		"tag: x-shadow-role",
		"literal:",
		"mongo-control-a.shadow-default-test.svc.cluster.local",
	}
	for _, c := range checks {
		if !strings.Contains(yaml, c) {
			t.Fatalf("expected %q in envoy yaml:\n%s", c, yaml)
		}
	}
	if strings.Contains(yaml, "transport_socket") {
		t.Fatal("mongo upstream must be cleartext TCP without transport_socket TLS")
	}
}
