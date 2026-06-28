package controller

import (
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"

	enginev1alpha1 "github.com/shadow-diff/monarch/api/v1alpha1"
)

func TestResolveNodeEntrypointModule_emptyCmdArgs(t *testing.T) {
	t.Parallel()
	got := resolveNodeEntrypointModule(corev1.Container{})
	if got != "/app/index.js" {
		t.Fatalf("got %q, want /app/index.js", got)
	}
}

func TestResolveNodeEntrypointModule_entrypointOnly(t *testing.T) {
	t.Parallel()
	got := resolveNodeEntrypointModule(corev1.Container{Command: []string{"node"}})
	if got != "/app/index.js" {
		t.Fatalf("got %q, want /app/index.js", got)
	}
}

func TestResolveNodeEntrypointModule_directoryArg(t *testing.T) {
	t.Parallel()
	got := resolveNodeEntrypointModule(corev1.Container{Args: []string{"/app"}})
	if got != "/app" {
		t.Fatalf("got %q, want /app", got)
	}
}

func TestResolveNodeEntrypointModule_dotArg(t *testing.T) {
	t.Parallel()
	got := resolveNodeEntrypointModule(corev1.Container{
		Args:       []string{"."},
		WorkingDir: "/srv",
	})
	if got != "/srv" {
		t.Fatalf("got %q, want /srv", got)
	}
}

func TestResolveNodeEntrypointModule_relativeWithWorkingDir(t *testing.T) {
	t.Parallel()
	got := resolveNodeEntrypointModule(corev1.Container{
		Command:    []string{"node"},
		Args:       []string{"index.js"},
		WorkingDir: "/srv",
	})
	if got != "/srv/index.js" {
		t.Fatalf("got %q, want /srv/index.js", got)
	}
}

func TestResolveNodeEntrypointModule_shellPlaceholder(t *testing.T) {
	t.Parallel()
	got := resolveNodeEntrypointModule(corev1.Container{
		Command: []string{"sh", "-c", "sleep infinity"},
	})
	if got != "/app/index.js" {
		t.Fatalf("got %q, want /app/index.js", got)
	}
}

func TestResolveNodeEntrypointModule_commandNodeWithScript(t *testing.T) {
	t.Parallel()
	got := resolveNodeEntrypointModule(corev1.Container{
		Command: []string{"node", "server.js"},
	})
	if got != "/app/server.js" {
		t.Fatalf("got %q, want /app/server.js", got)
	}
}

func TestBuildNodeWrapperJS(t *testing.T) {
	t.Parallel()
	got := buildNodeWrapperJS(`/app/it\'s.js`)
	if !strings.Contains(got, "MongoDBInstrumentation") {
		t.Fatalf("missing MongoDBInstrumentation: %q", got)
	}
	if !strings.Contains(got, "enhancedDatabaseReporting: true") {
		t.Fatalf("missing enhancedDatabaseReporting: %q", got)
	}
	if !strings.Contains(got, "sdk.start()") {
		t.Fatalf("missing sdk.start(): %q", got)
	}
	if !strings.Contains(got, `require('/app/it\\\'s.js')`) {
		t.Fatalf("unexpected require path: %q", got)
	}
}

func TestBuildTelemetryWrapperInitScript_heredoc(t *testing.T) {
	t.Parallel()
	wrapper := "process.env.FOO = '$BAR';\n`backtick`\n"
	script := buildTelemetryWrapperInitScript(wrapper)
	if !strings.Contains(script, "<<'MONARCH_WRAPPER_EOF'") {
		t.Fatal("missing quoted heredoc delimiter")
	}
	if !strings.Contains(script, "$BAR") {
		t.Fatal("wrapper $ should pass through verbatim")
	}
	if !strings.Contains(script, "`backtick`") {
		t.Fatal("wrapper backtick should pass through verbatim")
	}
}

func TestApplyNodeJSEntrypointWrapper(t *testing.T) {
	t.Parallel()
	podSpec := corev1.PodSpec{
		Volumes: []corev1.Volume{{
			Name: volumeNameEnvoyConfig,
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{Name: "envoy-cm"},
				},
			},
		}},
		Containers: []corev1.Container{
			{Name: containerApp, Image: "app:dev"},
			{Name: containerEnvoySidecar, Image: envoyImage, Args: []string{"-c", "/etc/envoy/envoy.yaml", "--log-level", "info"}},
		},
	}
	app := &podSpec.Containers[0]
	applyNodeJSEntrypointWrapper(&podSpec, app, "/app/index.js")

	if len(podSpec.InitContainers) != 1 || podSpec.InitContainers[0].Name != initContainerTelemetryWriter {
		t.Fatalf("initContainers = %#v", podSpec.InitContainers)
	}
	if podSpec.InitContainers[0].Image != busyboxImage {
		t.Fatalf("init image = %q", podSpec.InitContainers[0].Image)
	}
	foundVol := false
	for _, v := range podSpec.Volumes {
		if v.Name == volumeNameTelemetryOverride {
			foundVol = true
		}
	}
	if !foundVol {
		t.Fatal("missing telemetry-override volume")
	}
	if got := strings.Join(app.Command, ","); got != "node" {
		t.Fatalf("app.Command = %q", got)
	}
	if len(app.Args) != 1 || app.Args[0] != telemetryWrapperFile {
		t.Fatalf("app.Args = %#v", app.Args)
	}
	envoy := podSpec.Containers[1]
	if len(envoy.Command) != 0 || len(envoy.Args) == 0 || envoy.Args[0] != "-c" {
		t.Fatalf("envoy container mutated: cmd=%#v args=%#v", envoy.Command, envoy.Args)
	}
}

func TestNodeJSEntrypointWrapperEnabled(t *testing.T) {
	t.Parallel()
	st := &enginev1alpha1.ShadowTest{
		Spec: enginev1alpha1.ShadowTestSpec{Language: "nodejs"},
	}
	if !nodeJSEntrypointWrapperEnabled(st, "billing-service:v2") {
		t.Fatal("expected on for spec.language nodejs")
	}
	st.Spec.Language = "python"
	if nodeJSEntrypointWrapperEnabled(st, "node:20") {
		t.Fatal("expected off for spec.language python")
	}
	st.Spec.Language = ""
	if !nodeJSEntrypointWrapperEnabled(st, "node:20") {
		t.Fatal("expected on for image heuristic when language unset")
	}
}

func TestTargetPrimaryContainer(t *testing.T) {
	t.Parallel()
	dep := &appsv1.Deployment{
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name:       "prod",
						Args:       []string{"main.js"},
						WorkingDir: "/opt",
					}},
				},
			},
		},
	}
	c := targetPrimaryContainer(dep)
	if got := resolveNodeEntrypointModule(c); got != "/opt/main.js" {
		t.Fatalf("got %q", got)
	}
	if targetPrimaryContainer(nil).Name != "" {
		t.Fatal("nil target should return empty container")
	}
}

func TestBuildNodeWrapperJS_exactOutput(t *testing.T) {
	t.Parallel()
	want := `module.paths.unshift('/otel-auto-instrumentation/node_modules');

const { NodeSDK } = require('@opentelemetry/sdk-node');
const { MongoDBInstrumentation } = require('@opentelemetry/instrumentation-mongodb');

// Initialize the official OpenTelemetry SDK using stable public APIs
const sdk = new NodeSDK({
  instrumentations: [
    new MongoDBInstrumentation({
      enhancedDatabaseReporting: true
    })
  ]
});

// Start telemetry tracking safely before application boot
sdk.start();

// Boot the pristine application entrypoint module
require('/app/index.js');
`
	if got := buildNodeWrapperJS("/app/index.js"); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestResolveNodeEntrypointModule_noPanicOnNilSlices(t *testing.T) {
	t.Parallel()
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("panic: %v", r)
		}
	}()
	_ = resolveNodeEntrypointModule(corev1.Container{Name: "c"})
}
