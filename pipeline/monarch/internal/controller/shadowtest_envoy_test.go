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
		"beru_ingest",
		"beru-ingest.shadow-system.svc.cluster.local",
		"envoy.filters.http.lua",
		"envoy.filters.http.lua\", \"traceparent\"",
		"escape_json",
		"max_bytes = 65536",
		"httpCall",
		"/api/v1/ingest/wire",
		"request_body_mode: NONE",
		"name: egress_http_listener",
		"port_value: 10001",
		"dynamic_egress_cluster",
		"cluster: local_app",
		"port_value: 80",
		"port_value: 8080",
		"failure_mode_allow: true",
		"response_body_mode: BUFFERED",
	}
	for _, c := range checks {
		if !strings.Contains(yaml, c) {
			t.Fatalf("expected %q in envoy yaml:\n%s", c, yaml)
		}
	}
	if strings.Contains(yaml, "egress_stub") {
		t.Fatal("egress_stub should be replaced by always-on egress_http_listener")
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
	lua := strings.Index(section, "envoy.filters.http.lua")
	extProc := strings.Index(section, "envoy.filters.http.ext_proc")
	router := strings.Index(section, "envoy.filters.http.router")
	if lua < 0 || extProc < 0 || router < 0 {
		t.Fatalf("missing egress filters: lua=%d ext_proc=%d router=%d", lua, extProc, router)
	}
	if !(lua < extProc && extProc < router) {
		t.Fatalf("egress filter order must be lua → ext_proc → router; got lua=%d ext_proc=%d router=%d", lua, extProc, router)
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
			RecordAndReplay: []enginev1alpha1.RecordAndReplayHostSpec{
				{Host: "api.stripe.com", IgnoreRequestPaths: []string{"$.timestamp"}},
			},
		},
	}
	yaml, err := renderEnvoyYAML(st, "shadow-default-test", roleControlA)
	if err != nil {
		t.Fatal(err)
	}
	checks := []string{
		"name: egress_http_listener",
		"port_value: 10001",
		"envoy.filters.http.lua",
		"x-shadow-mode",
		"value: \"egress\"",
		"x-shadow-record-and-replay-config",
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
		t.Fatal("expected egress_http_listener, not egress_stub")
	}
	if strings.Contains(yaml, "name: external_apis") {
		t.Fatal("record/replay egress should not use passthrough external_apis virtual host")
	}
	assertEgressFilterOrder(t, yaml)
}

func TestRenderEnvoyYAML_egressProxyHostPort(t *testing.T) {
	st := &enginev1alpha1.ShadowTest{
		ObjectMeta: metav1.ObjectMeta{Name: "test"},
		Spec: enginev1alpha1.ShadowTestSpec{
			ServicePort:     80,
			ApplicationPort: 8080,
			BeruGRPCAddress: "beru.beru-system.svc.cluster.local:50051",
			BeruGRPCTimeout: "2s",
			RecordAndReplay: []enginev1alpha1.RecordAndReplayHostSpec{
				{Host: "user-service.prod:8080"},
			},
		},
	}
	yaml, err := renderEnvoyYAML(st, "shadow-default-test", roleControlA)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []string{
		"user-service.prod:8080",
		"user-service.prod:8080:*",
		`user-service.prod`,
		"x-shadow-record-and-replay-config",
	} {
		if !strings.Contains(yaml, c) {
			t.Fatalf("expected %q in envoy yaml:\n%s", c, yaml)
		}
	}
}

func TestParseRecordAndReplayTarget(t *testing.T) {
	host, port := parseRecordAndReplayTarget("api.example.com", defaultRecordAndReplayPort)
	if host != "api.example.com" || port != 80 {
		t.Fatalf("got %q:%d", host, port)
	}
	host, port = parseRecordAndReplayTarget("user-service.prod:8080", defaultRecordAndReplayPort)
	if host != "user-service.prod" || port != 8080 {
		t.Fatalf("got %q:%d", host, port)
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
	st := &enginev1alpha1.ShadowTest{}
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
	if !strings.Contains(found[envNoProxy], "beru-ingest.shadow-system.svc.cluster.local") {
		t.Fatalf("NO_PROXY must bypass beru-ingest: %q", found[envNoProxy])
	}
}

func TestEnvoySidecarEnvHasNoProxy(t *testing.T) {
	envoyEnv := []corev1.EnvVar{
		{Name: envShadowRole, Value: roleControlA},
		{Name: envBeruGRPCAddress, Value: defaultBeruGRPCAddress},
	}
	for _, e := range envoyEnv {
		switch e.Name {
		case envHTTPProxy, envHTTPSProxy, envNoProxy:
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

