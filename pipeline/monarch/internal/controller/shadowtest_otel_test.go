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
		t.Fatal("expected on by default")
	}
	disabled := false
	if otelInjectionEnabled(&enginev1alpha1.ShadowTest{
		Spec: enginev1alpha1.ShadowTestSpec{
			OtelInjection: &enginev1alpha1.OtelInjectionSpec{Enabled: &disabled},
		},
	}) {
		t.Fatal("expected off when enabled=false")
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
	if _, ok := ann[annotationOtelInjectSDK]; ok {
		t.Fatal("inject-sdk should be absent")
	}
	if ann[annotationOtelInjectPrefix+"java"] != otelInstrumentationName {
		t.Fatalf("inject-java = %q, want %q", ann[annotationOtelInjectPrefix+"java"], otelInstrumentationName)
	}
	if _, ok := ann[annotationOtelInjectPrefix+"python"]; ok {
		t.Fatal("inject-python should be absent for java workload")
	}
	if ann[annotationOtelContainerNames] != containerApp {
		t.Fatalf("container-names = %q", ann[annotationOtelContainerNames])
	}
	if ann[annotationOtelJavaContainerNames] != containerApp {
		t.Fatalf("java-container-names = %q", ann[annotationOtelJavaContainerNames])
	}
}

func TestOtelPodAnnotations_nodejsScopesAppContainer(t *testing.T) {
	t.Parallel()
	st := &enginev1alpha1.ShadowTest{
		Spec: enginev1alpha1.ShadowTestSpec{
			Language: "nodejs",
		},
	}
	ann := otelPodAnnotations(st, "http-rmq-test-app:dev")
	if ann[annotationOtelInjectPrefix+"nodejs"] != otelInstrumentationName {
		t.Fatalf("inject-nodejs = %q, want %q", ann[annotationOtelInjectPrefix+"nodejs"], otelInstrumentationName)
	}
	if _, ok := ann[annotationOtelInjectPrefix+"python"]; ok {
		t.Fatal("inject-python should be absent for nodejs workload")
	}
	if ann[annotationOtelNodeJSContainerNames] != containerApp {
		t.Fatalf("nodejs-container-names = %q", ann[annotationOtelNodeJSContainerNames])
	}
}

func TestSanitizeOtelInjectionAnnotations(t *testing.T) {
	t.Parallel()
	st := &enginev1alpha1.ShadowTest{Spec: enginev1alpha1.ShadowTestSpec{Language: "python"}}
	prod := map[string]string{
		annotationOtelInjectPrefix + "python": "prod-otel-cr",
		annotationOtelInjectSDK:               "true",
		"other.example.com/keep":              "yes",
	}
	ann := sanitizeOtelInjectionAnnotations(prod, st, "python:3.12")
	if ann["other.example.com/keep"] != "yes" {
		t.Fatal("non-otel annotation removed")
	}
	if _, ok := ann[annotationOtelInjectSDK]; ok {
		t.Fatal("inject-sdk should be stripped")
	}
	if ann[annotationOtelInjectPrefix+"python"] != otelInstrumentationName {
		t.Fatalf("inject-python = %q", ann[annotationOtelInjectPrefix+"python"])
	}
}

func TestOverwriteEnvByName(t *testing.T) {
	t.Parallel()
	base := []corev1.EnvVar{
		{Name: "OTEL_EXPORTER_OTLP_ENDPOINT", Value: "http://prod-collector:4317"},
		{Name: "FOO", Value: "bar"},
	}
	out := overwriteEnvByName(base,
		corev1.EnvVar{Name: "OTEL_EXPORTER_OTLP_ENDPOINT", Value: "http://beru-local:4317"},
		corev1.EnvVar{Name: "OTEL_PROPAGATORS", Value: "tracecontext"},
	)
	if got := envValue(out, "OTEL_EXPORTER_OTLP_ENDPOINT"); got != "http://beru-local:4317" {
		t.Fatalf("endpoint = %q", got)
	}
	if got := envValue(out, "FOO"); got != "bar" {
		t.Fatalf("FOO = %q", got)
	}
	if got := envValue(out, "OTEL_PROPAGATORS"); got != "tracecontext" {
		t.Fatalf("propagators = %q", got)
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
			Language: "nodejs",
			Dependencies: []enginev1alpha1.DependencySpec{{
				Name: "mongo", Type: "mongodb", Image: "mongo:4.4", Port: 27017, EnvVarInjection: "MONGO_URL",
			}},
		},
	}
	envs := otelEnvVars(st, shadowNamespaceForCR(st), roleControlA, "nodejs-test-worker:dev")
	if got := envValue(envs, envOtelTracesExporter); got != "otlp" {
		t.Fatalf("OTEL_TRACES_EXPORTER = %q, want otlp", got)
	}
	wantOTLP := beruOTLPEndpointFor(st, shadowNamespaceForCR(st))
	if got := envValue(envs, envOtelExporterOTLPEndpoint); got != wantOTLP {
		t.Fatalf("OTEL_EXPORTER_OTLP_ENDPOINT = %q, want %q", got, wantOTLP)
	}
	if got := envValue(envs, envOtelExporterOTLPProtocol); got != "grpc" {
		t.Fatalf("OTEL_EXPORTER_OTLP_PROTOCOL = %q", got)
	}
	if got := envValue(envs, envOtelExporterOTLPTracesProtocol); got != "grpc" {
		t.Fatalf("OTEL_EXPORTER_OTLP_TRACES_PROTOCOL = %q", got)
	}
	if got := envValue(envs, envOtelNodeEnabledInstrumentations); got != "http" {
		t.Fatalf("OTEL_NODE_ENABLED_INSTRUMENTATIONS = %q, want http (mongo via entrypoint wrapper)", got)
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
			Language: "python",
			Dependencies: []enginev1alpha1.DependencySpec{{
				Name: "mongo", Type: "mongodb", Image: "mongo:4.4", Port: 27017, EnvVarInjection: "MONGO_URL",
			}},
		},
	}
	envs := otelEnvVars(st, shadowNamespaceForCR(st), roleControlA, "python-test-worker:dev")
	if got := envValue(envs, envOtelExporterOTLPEndpoint); got != beruOTLPHTTPEndpointFor(st, shadowNamespaceForCR(st)) {
		t.Fatalf("OTEL_EXPORTER_OTLP_ENDPOINT = %q", got)
	}
	if got := envValue(envs, envOtelExporterOTLPTracesProtocol); got != "http/protobuf" {
		t.Fatalf("OTEL_EXPORTER_OTLP_TRACES_PROTOCOL = %q", got)
	}
	if got := envValue(envs, envOtelNodeEnabledInstrumentations); got != "" {
		t.Fatalf("OTEL_NODE_ENABLED_INSTRUMENTATIONS = %q, want empty for python", got)
	}
	if got := envValue(envs, "OTEL_PYTHON_DISABLED_INSTRUMENTATIONS"); got != "" {
		t.Fatalf("OTEL_PYTHON_DISABLED_INSTRUMENTATIONS = %q, want absent", got)
	}
	if got := envValue(envs, envOtelPythonMongoCaptureStatement); got != "true" {
		t.Fatalf("OTEL_PYTHON_MONGODB_CAPTURE_STATEMENT = %q", got)
	}
}

func TestOtelEnvVars_noMongoUsesNoneExporter(t *testing.T) {
	t.Parallel()
	st := &enginev1alpha1.ShadowTest{ObjectMeta: metav1.ObjectMeta{Name: "http-shadow"}}
	if got := envValue(otelEnvVars(st, shadowNamespaceForCR(st), roleControlA, "app:latest"), envOtelTracesExporter); got != "none" {
		t.Fatalf("OTEL_TRACES_EXPORTER = %q, want none", got)
	}
}

func TestOtelEnvVars_pythonRabbitMQEnablesPikaPropagation(t *testing.T) {
	t.Parallel()
	st := &enginev1alpha1.ShadowTest{
		ObjectMeta: metav1.ObjectMeta{Name: "http-otel-rmq-python-shadow"},
		Spec: enginev1alpha1.ShadowTestSpec{
			Language: "python",
			Dependencies: []enginev1alpha1.DependencySpec{{
				Name: "rabbitmq", Type: "rabbitmq", EnvVarInjection: "AMQP_URL",
			}},
		},
	}
	envs := otelEnvVars(st, shadowNamespaceForCR(st), roleControlA, "http-rmq-python-worker:dev")
	if got := envValue(envs, envOtelTracesExporter); got != "otlp" {
		t.Fatalf("OTEL_TRACES_EXPORTER = %q, want otlp", got)
	}
	if got := envValue(envs, envOtelPythonEnabledInstrumentations); got != "flask,pika" {
		t.Fatalf("OTEL_PYTHON_ENABLED_INSTRUMENTATIONS = %q", got)
	}
}

func TestOtelEnvVars_nodejsRabbitMQEnablesAmqplib(t *testing.T) {
	t.Parallel()
	st := &enginev1alpha1.ShadowTest{
		ObjectMeta: metav1.ObjectMeta{Name: "http-otel-rmq-nodejs-shadow"},
		Spec: enginev1alpha1.ShadowTestSpec{
			Language: "nodejs",
			Dependencies: []enginev1alpha1.DependencySpec{{
				Name: "rabbitmq", Type: "rabbitmq", EnvVarInjection: "AMQP_URL",
			}},
		},
	}
	envs := otelEnvVars(st, shadowNamespaceForCR(st), roleControlA, "http-rmq-test-app:dev")
	if got := envValue(envs, envOtelTracesExporter); got != "none" {
		t.Fatalf("OTEL_TRACES_EXPORTER = %q, want none", got)
	}
	if got := envValue(envs, envOtelNodeEnabledInstrumentations); got != "amqplib,http" {
		t.Fatalf("OTEL_NODE_ENABLED_INSTRUMENTATIONS = %q", got)
	}
}

func TestOtelEnvVars_nodejsMongoAndRabbitMQ(t *testing.T) {
	t.Parallel()
	st := &enginev1alpha1.ShadowTest{
		ObjectMeta: metav1.ObjectMeta{Name: "http-otel-rmq-nodejs-shadow"},
		Spec: enginev1alpha1.ShadowTestSpec{
			Language: "nodejs",
			Dependencies: []enginev1alpha1.DependencySpec{
				{Name: "rabbitmq", Type: "rabbitmq", EnvVarInjection: "AMQP_URL"},
				{Name: "mongodb", Type: "mongodb", Image: "mongo:4.4", Port: 27017, EnvVarInjection: "MONGO_URL"},
			},
		},
	}
	envs := otelEnvVars(st, shadowNamespaceForCR(st), roleControlA, "http-rmq-test-app:dev")
	if got := envValue(envs, envOtelTracesExporter); got != "otlp" {
		t.Fatalf("OTEL_TRACES_EXPORTER = %q, want otlp", got)
	}
	if got := envValue(envs, envOtelNodeEnabledInstrumentations); got != "http,amqplib" {
		t.Fatalf("OTEL_NODE_ENABLED_INSTRUMENTATIONS = %q, want http,amqplib (mongo via entrypoint wrapper)", got)
	}
}

func TestOtelEnvVars_pythonMongoAndRabbitMQ(t *testing.T) {
	t.Parallel()
	st := &enginev1alpha1.ShadowTest{
		ObjectMeta: metav1.ObjectMeta{Name: "http-otel-rmq-python-shadow"},
		Spec: enginev1alpha1.ShadowTestSpec{
			Language: "python",
			Dependencies: []enginev1alpha1.DependencySpec{
				{Name: "rabbitmq", Type: "rabbitmq", EnvVarInjection: "AMQP_URL"},
				{Name: "mongodb", Type: "mongodb", Image: "mongo:4.4", Port: 27017, EnvVarInjection: "MONGO_URL"},
			},
		},
	}
	envs := otelEnvVars(st, shadowNamespaceForCR(st), roleControlA, "http-rmq-python-worker:dev")
	if got := envValue(envs, envOtelTracesExporter); got != "otlp" {
		t.Fatalf("OTEL_TRACES_EXPORTER = %q, want otlp", got)
	}
	if got := envValue(envs, envOtelPythonEnabledInstrumentations); got != "flask,pika,pymongo" {
		t.Fatalf("OTEL_PYTHON_ENABLED_INSTRUMENTATIONS = %q", got)
	}
	if got := envValue(envs, envOtelPythonMongoCaptureStatement); got != "true" {
		t.Fatalf("OTEL_PYTHON_MONGODB_CAPTURE_STATEMENT = %q", got)
	}
}
