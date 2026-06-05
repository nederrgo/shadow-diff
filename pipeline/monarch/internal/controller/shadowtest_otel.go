package controller

import (
	"strings"

	corev1 "k8s.io/api/core/v1"

	enginev1alpha1 "github.com/shadow-diff/monarch/api/v1alpha1"
)

const (
	annotationOtelInjectPrefix = "instrumentation.opentelemetry.io/inject-"
	annotationOtelInjectSDK    = annotationOtelInjectPrefix + "sdk"

	envOtelTracesExporter  = "OTEL_TRACES_EXPORTER"
	envOtelMetricsExporter = "OTEL_METRICS_EXPORTER"
	envOtelLogsExporter    = "OTEL_LOGS_EXPORTER"
	envOtelServiceName     = "OTEL_SERVICE_NAME"
	envOtelPropagators     = "OTEL_PROPAGATORS"
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
		annotationOtelInjectSDK: "true",
	}
	if lang := detectOtelLanguage(appImage, otelLanguageFromSpec(st)); lang != "" {
		ann[annotationOtelInjectPrefix+lang] = "true"
	}
	return ann
}

func otelEnvVars(st *enginev1alpha1.ShadowTest, role string) []corev1.EnvVar {
	name := st.Name + "-" + role
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
