package controller

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	enginev1alpha1 "github.com/shadow-diff/monarch/api/v1alpha1"
)

func TestOtelInjectionEnabled(t *testing.T) {
	t.Parallel()
	if !otelInjectionEnabled(&enginev1alpha1.ShadowTest{}) {
		t.Fatal("expected default on")
	}
	disabled := false
	if otelInjectionEnabled(&enginev1alpha1.ShadowTest{
		Spec: enginev1alpha1.ShadowTestSpec{
			OtelInjection: &enginev1alpha1.OtelInjectionSpec{Enabled: &disabled},
		},
	}) {
		t.Fatal("expected disabled")
	}
}

func TestDetectOtelLanguage(t *testing.T) {
	t.Parallel()
	cases := []struct {
		image string
		spec  string
		want  string
	}{
		{"eclipse-temurin:17", "", "java"},
		{"python:3.12", "", "python"},
		{"node:20", "", "nodejs"},
		{"mcr.microsoft.com/dotnet/aspnet:8.0", "", "dotnet"},
		{"golang:1.22", "", "go"},
		{"shadow-diff/app:latest", "", ""},
		{"python:3.12", "java", "java"},
	}
	for _, tc := range cases {
		if got := detectOtelLanguage(tc.image, tc.spec); got != tc.want {
			t.Fatalf("detectOtelLanguage(%q, %q) = %q, want %q", tc.image, tc.spec, got, tc.want)
		}
	}
}

func TestOtelPodAnnotations(t *testing.T) {
	t.Parallel()
	st := &enginev1alpha1.ShadowTest{}
	st.Name = "st"
	ann := otelPodAnnotations(st, "eclipse-temurin:17")
	if ann[annotationOtelInjectSDK] != "true" {
		t.Fatal("missing inject-sdk")
	}
	if ann[annotationOtelInjectPrefix+"java"] != "true" {
		t.Fatal("missing inject-java")
	}
}

func TestOtelPodAnnotations_disabledNotAppliedInReconcile(t *testing.T) {
	t.Parallel()
	disabled := false
	st := &enginev1alpha1.ShadowTest{
		Spec: enginev1alpha1.ShadowTestSpec{
			OtelInjection: &enginev1alpha1.OtelInjectionSpec{Enabled: &disabled},
		},
	}
	if otelInjectionEnabled(st) {
		t.Fatal("expected false")
	}
}

func envValue(envs []corev1.EnvVar, name string) string {
	for _, e := range envs {
		if e.Name == name {
			return e.Value
		}
	}
	return ""
}

func TestOtelEnvVars_mongoDependencyExportsOTLP(t *testing.T) {
	t.Parallel()
	st := &enginev1alpha1.ShadowTest{
		ObjectMeta: metav1.ObjectMeta{Name: "mongo-test-shadow"},
		Spec: enginev1alpha1.ShadowTestSpec{
			OtelInjection: &enginev1alpha1.OtelInjectionSpec{
				Language: "nodejs",
			},
			Dependencies: []enginev1alpha1.DependencySpec{{
				Name: "mongo", Image: "mongo:4.4", Port: 27017, EnvVarInjection: "MONGO_URL",
			}},
		},
	}
	envs := otelEnvVars(st, roleControlA, "nodejs-test-worker:dev")
	if got := envValue(envs, envOtelTracesExporter); got != "otlp" {
		t.Fatalf("OTEL_TRACES_EXPORTER = %q, want otlp", got)
	}
	if got := envValue(envs, envOtelExporterOTLPEndpoint); got != defaultBeruOTLPEndpoint {
		t.Fatalf("OTEL_EXPORTER_OTLP_ENDPOINT = %q", got)
	}
	if got := envValue(envs, envOtelExporterOTLPProtocol); got != "grpc" {
		t.Fatalf("OTEL_EXPORTER_OTLP_PROTOCOL = %q", got)
	}
	if got := envValue(envs, envOtelExporterOTLPTracesProtocol); got != "grpc" {
		t.Fatalf("OTEL_EXPORTER_OTLP_TRACES_PROTOCOL = %q", got)
	}
	if got := envValue(envs, envOtelNodeEnabledInstrumentations); got != "mongodb,http" {
		t.Fatalf("OTEL_NODE_ENABLED_INSTRUMENTATIONS = %q", got)
	}
	if got := envValue(envs, envOtelServiceName); got != "mongo-test-shadow-control-a" {
		t.Fatalf("OTEL_SERVICE_NAME = %q", got)
	}
}

func TestOtelEnvVars_pythonMongoOmitsNodeInstrumentations(t *testing.T) {
	t.Parallel()
	st := &enginev1alpha1.ShadowTest{
		ObjectMeta: metav1.ObjectMeta{Name: "python-hybrid-shadow"},
		Spec: enginev1alpha1.ShadowTestSpec{
			OtelInjection: &enginev1alpha1.OtelInjectionSpec{Language: "python"},
			Dependencies: []enginev1alpha1.DependencySpec{{
				Name: "mongo", Image: "mongo:4.4", Port: 27017, EnvVarInjection: "MONGO_URL",
			}},
		},
	}
	envs := otelEnvVars(st, roleControlA, "python-test-worker:dev")
	if got := envValue(envs, envOtelExporterOTLPEndpoint); got != defaultBeruOTLPHTTPEndpoint {
		t.Fatalf("OTEL_EXPORTER_OTLP_ENDPOINT = %q", got)
	}
	if got := envValue(envs, envOtelExporterOTLPTracesProtocol); got != "http/protobuf" {
		t.Fatalf("OTEL_EXPORTER_OTLP_TRACES_PROTOCOL = %q", got)
	}
	if got := envValue(envs, envOtelNodeEnabledInstrumentations); got != "" {
		t.Fatalf("OTEL_NODE_ENABLED_INSTRUMENTATIONS = %q, want empty for python", got)
	}
	if got := envValue(envs, envOtelPythonDisabledInstrumentations); got != "pika" {
		t.Fatalf("OTEL_PYTHON_DISABLED_INSTRUMENTATIONS = %q", got)
	}
	if got := envValue(envs, envOtelPythonMongoCaptureStatement); got != "true" {
		t.Fatalf("OTEL_PYTHON_MONGODB_CAPTURE_STATEMENT = %q", got)
	}
}

func TestOtelEnvVars_noMongoUsesNoneExporter(t *testing.T) {
	t.Parallel()
	st := &enginev1alpha1.ShadowTest{ObjectMeta: metav1.ObjectMeta{Name: "http-shadow"}}
	if got := envValue(otelEnvVars(st, roleControlA, "app:latest"), envOtelTracesExporter); got != "none" {
		t.Fatalf("OTEL_TRACES_EXPORTER = %q, want none", got)
	}
}
