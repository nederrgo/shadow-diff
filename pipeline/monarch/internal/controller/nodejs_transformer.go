package controller

import (
	"path"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"

	enginev1alpha1 "github.com/shadow-diff/monarch/api/v1alpha1"
)

const (
	volumeNameTelemetryOverride        = "telemetry-override"
	initContainerTelemetryWriter       = "telemetry-wrapper-writer"
	telemetryWrapperMountPath          = "/telemetry-override"
	telemetryWrapperFile               = "/telemetry-override/wrapper.js"
	busyboxImage                       = "busybox:1.36"
	defaultNodeWorkingDir              = "/app"
	defaultNodeEntryModule             = "index.js"
	otelAutoInstrumentationNodeModules = "/otel-auto-instrumentation/node_modules"
)

func nodeJSEntrypointWrapperEnabled(st *enginev1alpha1.ShadowTest, image string) bool {
	if otelLanguageFromSpec(st) == "nodejs" {
		return true
	}
	// ponytail: image heuristic fallback only when spec.language is unset
	return otelLanguageFromSpec(st) == "" && detectOtelLanguage(image, "") == "nodejs"
}

func nodeWorkingDir(c corev1.Container) string {
	if wd := strings.TrimSpace(c.WorkingDir); wd != "" {
		return wd
	}
	return defaultNodeWorkingDir
}

func isShellInterpreter(cmd string) bool {
	base := strings.ToLower(strings.TrimSpace(path.Base(cmd)))
	switch base {
	case "sh", "bash", "dash", "zsh", "ash":
		return true
	default:
		return false
	}
}

func isNodeRuntime(cmd string) bool {
	base := strings.ToLower(strings.TrimSpace(path.Base(cmd)))
	return base == "node"
}

func toRequireModuleArg(raw, workingDir string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "." {
		return workingDir
	}
	if strings.HasPrefix(raw, "/") {
		return raw
	}
	return path.Join(workingDir, raw)
}

func resolveNodeEntrypointModule(c corev1.Container) string {
	workingDir := nodeWorkingDir(c)
	var raw string
	switch {
	case len(c.Args) > 0:
		raw = c.Args[0]
	case len(c.Command) > 1 && isNodeRuntime(c.Command[0]):
		raw = c.Command[1]
	case len(c.Command) > 0 && !isNodeRuntime(c.Command[0]) && !isShellInterpreter(c.Command[0]):
		raw = c.Command[0]
	}
	if raw == "" {
		return path.Join(workingDir, defaultNodeEntryModule)
	}
	return toRequireModuleArg(raw, workingDir)
}

func escapeJSSingleQuoted(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `'`, `\'`)
	return s
}

func buildNodeWrapperJS(entrypointModule string) string {
	entry := escapeJSSingleQuoted(entrypointModule)
	var b strings.Builder
	// ponytail: wrapper.js lives outside /app; OTel operator copies SDK packages to emptyDir
	b.WriteString("module.paths.unshift('")
	b.WriteString(otelAutoInstrumentationNodeModules)
	b.WriteString("');\n\n")
	b.WriteString("const { NodeSDK } = require('@opentelemetry/sdk-node');\n")
	b.WriteString("const { MongoDBInstrumentation } = require('@opentelemetry/instrumentation-mongodb');\n\n")
	b.WriteString("// Initialize the official OpenTelemetry SDK using stable public APIs\n")
	b.WriteString("const sdk = new NodeSDK({\n")
	b.WriteString("  instrumentations: [\n")
	b.WriteString("    new MongoDBInstrumentation({\n")
	b.WriteString("      enhancedDatabaseReporting: true\n")
	b.WriteString("    })\n")
	b.WriteString("  ]\n")
	b.WriteString("});\n\n")
	b.WriteString("// Start telemetry tracking safely before application boot\n")
	b.WriteString("sdk.start();\n\n")
	b.WriteString("// Boot the pristine application entrypoint module\n")
	b.WriteString("require('")
	b.WriteString(entry)
	b.WriteString("');\n")
	return b.String()
}

func buildTelemetryWrapperInitScript(wrapperJS string) string {
	var b strings.Builder
	b.WriteString("set -eu\n")
	b.WriteString("mkdir -p ")
	b.WriteString(telemetryWrapperMountPath)
	b.WriteString("\ncat > ")
	b.WriteString(telemetryWrapperFile)
	b.WriteString(" <<'MONARCH_WRAPPER_EOF'\n")
	b.WriteString(wrapperJS)
	if !strings.HasSuffix(wrapperJS, "\n") {
		b.WriteByte('\n')
	}
	b.WriteString("MONARCH_WRAPPER_EOF\n")
	return b.String()
}

func appendVolumeIfMissing(podSpec *corev1.PodSpec, vol corev1.Volume) {
	for i := range podSpec.Volumes {
		if podSpec.Volumes[i].Name == vol.Name {
			return
		}
	}
	podSpec.Volumes = append(podSpec.Volumes, vol)
}

func appendInitContainerIfMissing(podSpec *corev1.PodSpec, init corev1.Container) {
	for i := range podSpec.InitContainers {
		if podSpec.InitContainers[i].Name == init.Name {
			podSpec.InitContainers[i] = init
			return
		}
	}
	podSpec.InitContainers = append(podSpec.InitContainers, init)
}

func appendVolumeMountIfMissing(c *corev1.Container, mount corev1.VolumeMount) {
	for i := range c.VolumeMounts {
		if c.VolumeMounts[i].Name == mount.Name {
			return
		}
	}
	c.VolumeMounts = append(c.VolumeMounts, mount)
}

func applyNodeJSEntrypointWrapper(podSpec *corev1.PodSpec, app *corev1.Container, entrypointModule string) {
	wrapperJS := buildNodeWrapperJS(entrypointModule)
	script := buildTelemetryWrapperInitScript(wrapperJS)

	appendVolumeIfMissing(podSpec, corev1.Volume{
		Name: volumeNameTelemetryOverride,
		VolumeSource: corev1.VolumeSource{
			EmptyDir: &corev1.EmptyDirVolumeSource{},
		},
	})
	appendInitContainerIfMissing(podSpec, corev1.Container{
		Name:    initContainerTelemetryWriter,
		Image:   busyboxImage,
		Command: []string{"/bin/sh", "-c", script},
		VolumeMounts: []corev1.VolumeMount{{
			Name:      volumeNameTelemetryOverride,
			MountPath: telemetryWrapperMountPath,
		}},
	})
	appendVolumeMountIfMissing(app, corev1.VolumeMount{
		Name:      volumeNameTelemetryOverride,
		MountPath: telemetryWrapperMountPath,
		ReadOnly:  true,
	})
	app.Command = []string{"node"}
	app.Args = []string{telemetryWrapperFile}
}

func targetPrimaryContainer(target *appsv1.Deployment) corev1.Container {
	if target != nil && len(target.Spec.Template.Spec.Containers) > 0 {
		return target.Spec.Template.Spec.Containers[0]
	}
	return corev1.Container{}
}
