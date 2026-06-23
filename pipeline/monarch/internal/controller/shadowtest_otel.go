package controller

import (
	"strings"

	corev1 "k8s.io/api/core/v1"

	enginev1alpha1 "github.com/shadow-diff/monarch/api/v1alpha1"
)

const (
	annotationOtelInjectPrefix          = "instrumentation.opentelemetry.io/inject-"
	annotationOtelInjectSDK             = annotationOtelInjectPrefix + "sdk"
	annotationOtelContainerNames        = "instrumentation.opentelemetry.io/container-names"
	annotationOtelNodeJSContainerNames  = "instrumentation.opentelemetry.io/nodejs-container-names"
	annotationOtelPythonContainerNames  = "instrumentation.opentelemetry.io/python-container-names"
	annotationOtelJavaContainerNames    = "instrumentation.opentelemetry.io/java-container-names"
	annotationOtelDotNetContainerNames = "instrumentation.opentelemetry.io/dotnet-container-names"
	annotationOtelGoContainerNames      = "instrumentation.opentelemetry.io/go-container-names"

	envOtelTracesExporter                 = "OTEL_TRACES_EXPORTER"
	envOtelMetricsExporter                = "OTEL_METRICS_EXPORTER"
	envOtelLogsExporter                   = "OTEL_LOGS_EXPORTER"
	envOtelServiceName                    = "OTEL_SERVICE_NAME"
	envOtelPropagators                    = "OTEL_PROPAGATORS"
	envOtelExporterOTLPEndpoint           = "OTEL_EXPORTER_OTLP_ENDPOINT"
	envOtelExporterOTLPProtocol           = "OTEL_EXPORTER_OTLP_PROTOCOL"
	envOtelExporterOTLPTracesProtocol     = "OTEL_EXPORTER_OTLP_TRACES_PROTOCOL"
	envOtelNodeEnabledInstrumentations    = "OTEL_NODE_ENABLED_INSTRUMENTATIONS"
	envOtelPythonMongoCaptureStatement    = "OTEL_PYTHON_MONGODB_CAPTURE_STATEMENT"

	defaultBeruOTLPEndpoint     = "http://beru.beru-system.svc.cluster.local:4317"
	defaultBeruOTLPHTTPEndpoint = "http://beru.beru-system.svc.cluster.local:8080"
)

func otelInjectionEnabled(st *enginev1alpha1.ShadowTest) bool {
	if st == nil || st.Spec.OtelInjection == nil || st.Spec.OtelInjection.Enabled == nil {
		return true
	}
	return *st.Spec.OtelInjection.Enabled
}

func otelLanguageFromSpec(st *enginev1alpha1.ShadowTest) string {
	if st == nil || st.Spec.OtelInjection == nil {
		return ""
	}
	return strings.TrimSpace(st.Spec.OtelInjection.Language)
}

// detectOtelLanguage returns a language key for inject-<lang> annotations, or "" for sdk-only.
func detectOtelLanguage(image, specLang string) string {
	if specLang != "" {
		return specLang
	}
	img := strings.ToLower(image)
	switch {
	case strings.Contains(img, "openjdk"), strings.Contains(img, "temurin"),
		strings.Contains(img, "corretto"), strings.Contains(img, "java"):
		return "java"
	case strings.Contains(img, "python"):
		return "python"
	case strings.Contains(img, "nodejs"), strings.Contains(img, "node:"), strings.Contains(img, "/node"):
		return "nodejs"
	case strings.Contains(img, "dotnet"), strings.Contains(img, "aspnet"):
		return "dotnet"
	case strings.Contains(img, "golang"), strings.Contains(img, ":go"), strings.Contains(img, "/go"):
		return "go"
	default:
		return ""
	}
}

func otelPodAnnotations(st *enginev1alpha1.ShadowTest, appImage string) map[string]string {
	ann := map[string]string{
		annotationOtelInjectSDK:      "true",
		annotationOtelContainerNames: containerApp,
		annotationOtelInjectPrefix + "sdk-container-names": containerApp,
	}
	if lang := detectOtelLanguage(appImage, otelLanguageFromSpec(st)); lang != "" {
		ann[annotationOtelInjectPrefix+lang] = "true"
		if key := otelLanguageContainerNamesAnnotation(lang); key != "" {
			ann[key] = containerApp
		}
	}
	return ann
}

func otelLanguageContainerNamesAnnotation(lang string) string {
	switch lang {
	case "nodejs":
		return annotationOtelNodeJSContainerNames
	case "python":
		return annotationOtelPythonContainerNames
	case "java":
		return annotationOtelJavaContainerNames
	case "dotnet":
		return annotationOtelDotNetContainerNames
	case "go":
		return annotationOtelGoContainerNames
	default:
		return ""
	}
}

func otelEnvVars(st *enginev1alpha1.ShadowTest, role, appImage string) []corev1.EnvVar {
	name := st.Name + "-" + role
	if otelInjectionEnabled(st) && hasMongoDependency(st) {
		lang := detectOtelLanguage(appImage, otelLanguageFromSpec(st))
		endpoint := defaultBeruOTLPEndpoint
		protocol := "grpc"
		tracesProtocol := "grpc"
		if lang == "python" {
			endpoint = defaultBeruOTLPHTTPEndpoint
			protocol = "http/protobuf"
			tracesProtocol = "http/protobuf"
		}
		envs := []corev1.EnvVar{
			{Name: envOtelTracesExporter, Value: "otlp"},
			{Name: envOtelMetricsExporter, Value: "none"},
			{Name: envOtelLogsExporter, Value: "none"},
			{Name: envOtelServiceName, Value: name},
			{Name: envOtelPropagators, Value: "tracecontext"},
			{Name: envOtelExporterOTLPEndpoint, Value: endpoint},
			{Name: envOtelExporterOTLPProtocol, Value: protocol},
			{Name: envOtelExporterOTLPTracesProtocol, Value: tracesProtocol},
		}
		if lang == "nodejs" {
			envs = append(envs, corev1.EnvVar{
				Name: envOtelNodeEnabledInstrumentations, Value: "mongodb,http",
			})
		}
		if lang == "python" {
			envs = append(envs, corev1.EnvVar{
				Name: envOtelPythonMongoCaptureStatement, Value: "true",
			})
		}
		return envs
	}
	return []corev1.EnvVar{
		{Name: envOtelTracesExporter, Value: "none"},
		{Name: envOtelMetricsExporter, Value: "none"},
		{Name: envOtelLogsExporter, Value: "none"},
		{Name: envOtelServiceName, Value: name},
		{Name: envOtelPropagators, Value: "tracecontext"},
	}
}

func mergeAnnotations(base, extra map[string]string) map[string]string {
	out := make(map[string]string, len(base)+len(extra))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range extra {
		out[k] = v
	}
	return out
}
