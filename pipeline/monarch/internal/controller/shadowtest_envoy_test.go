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
		"x-shadow-role",
		"value: \"control-a\"",
		"initial_metadata",
		"envoy.extensions.filters.http.ext_proc.v3.ExternalProcessor",
		"envoy.filters.http.ext_proc",
		"envoy.filters.http.header_mutation",
		"beru_ext_proc",
		"request_body_mode: NONE",
		"name: egress_http_listener",
		"port_value: 10001",
		"address: 0.0.0.0",
		"cluster: local_app",
		"port_value: 80",
		"port_value: 8080",
		"failure_mode_allow: true",
		"response_body_mode: BUFFERED",
		"egress_passthrough",
		"direct_response",
		"status: 502",
		"shop_ext_proc",
		"failure_mode_allow: false",
	}
	for _, c := range checks {
		if !strings.Contains(yaml, c) {
			t.Fatalf("expected %q in envoy yaml:\n%s", c, yaml)
		}
	}
	for _, forbidden := range []string{
		"egress_stub", "egress_blackhole", "beru_ingest", "envoy.filters.http.lua",
		"dynamic_egress_cluster", "x-shadow-record-and-replay-config",
	} {
		if strings.Contains(yaml, forbidden) {
			t.Fatalf("envoy yaml must not contain %q:\n%s", forbidden, yaml)
		}
	}
	assertEgressFilterOrder(t, yaml)
}

func assertEgressFilterOrder(t *testing.T, yaml string) {
	t.Helper()
	idx := strings.Index(yaml, "name: egress_http_listener")
	if idx < 0 {
		t.Fatal("missing egress_http_listener")
	}
	section := yaml[idx:]
	extProc := strings.Index(section, "envoy.filters.http.ext_proc")
	router := strings.Index(section, "envoy.filters.http.router")
	if extProc < 0 || router < 0 {
		t.Fatalf("missing egress filters: ext_proc=%d router=%d", extProc, router)
	}
	if extProc >= router {
		t.Fatalf("egress filter order must be ext_proc → router; got ext_proc=%d router=%d", extProc, router)
	}
}

func TestRenderEnvoyYAML_localBeruGRPC(t *testing.T) {
	t.Parallel()
	const shadowNS = "shadow-default-my-test"
	st := &enginev1alpha1.ShadowTest{
		ObjectMeta: metav1.ObjectMeta{Name: "my-test", Namespace: "default"},
		Spec: enginev1alpha1.ShadowTestSpec{
			ServicePort:     80,
			ApplicationPort: 8080,
		},
	}
	yaml, err := renderEnvoyYAML(st, shadowNS, roleControlA)
	if err != nil {
		t.Fatal(err)
	}
	wantHost := "beru-local.shadow-default-my-test.svc.cluster.local"
	if !strings.Contains(yaml, wantHost) {
		t.Fatalf("expected local Beru host %q in envoy yaml:\n%s", wantHost, yaml)
	}
	if strings.Contains(yaml, "http://"+wantHost) {
		t.Fatal("envoy beru cluster must use bare host, not http:// URI")
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
		},
	}
	yaml, err := renderEnvoyYAML(st, "shadow-default-test", roleControlA)
	if err != nil {
		t.Fatal(err)
	}
	checks := []string{
		"name: egress_http_listener",
		"port_value: 10001",
		"address: 0.0.0.0",
		"x-shadow-mode",
		"value: \"egress\"",
		"request_body_mode: BUFFERED",
		"response_body_mode: NONE",
		"failure_mode_allow: false",
		"shop_ext_proc",
		"egress_passthrough",
		"direct_response",
		"status: 502",
	}
	for _, c := range checks {
		if !strings.Contains(yaml, c) {
			t.Fatalf("expected %q in envoy yaml:\n%s", c, yaml)
		}
	}
	idx := strings.Index(yaml, "name: egress_http_listener")
	if idx < 0 {
		t.Fatal("missing egress_http_listener")
	}
	egressSection := yaml[idx:]
	if !strings.Contains(egressSection, "request_body_mode: BUFFERED") {
		t.Fatal("egress listener must buffer request body for Shop→Beru reporting")
	}
	for _, forbidden := range []string{
		"egress_stub", "egress_blackhole", "beru_ingest", "envoy.filters.http.lua",
		"x-shadow-record-and-replay-config", "api.stripe.com:*",
		"name: external_apis", "name: egress_record_and_replay",
	} {
		if strings.Contains(yaml, forbidden) {
			t.Fatalf("envoy yaml must not contain %q:\n%s", forbidden, yaml)
		}
	}
	assertEgressFilterOrder(t, yaml)
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

func TestEnvoySidecarEnvHasNoProxy(t *testing.T) {
	envoyEnv := []corev1.EnvVar{
		{Name: envShadowRole, Value: roleControlA},
		{Name: envBeruGRPCAddress, Value: defaultBeruGRPCAddress},
	}
	for _, e := range envoyEnv {
		if e.Name == "HTTP_PROXY" || e.Name == "HTTPS_PROXY" || e.Name == "NO_PROXY" {
			t.Fatalf("envoy-sidecar must not have proxy env %q", e.Name)
		}
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
				Name: "mongo", Type: "mongodb", Image: "mongo:7", Port: 27017, EnvVarInjection: "MONGO_URL",
			}},
		},
	}
	yaml, err := renderEnvoyYAML(st, "shadow-default-test", roleControlA)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"mongo_egress", "mongo_proxy", "mongo_upstream"} {
		if strings.Contains(yaml, forbidden) {
			t.Fatalf("envoy yaml must not contain %q (L4 MongoDB removed):\n%s", forbidden, yaml)
		}
	}
}
