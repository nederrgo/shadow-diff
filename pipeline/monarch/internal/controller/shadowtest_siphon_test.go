package controller

import (
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	enginev1alpha1 "github.com/shadow-diff/monarch/api/v1alpha1"
)

func TestShadowSiphonOTelEndpoint(t *testing.T) {
	want := "siphon.shadow-default-my-st.svc.cluster.local:4317"
	if got := shadowSiphonOTelEndpoint("shadow-default-my-st"); got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestSiphonMaxPayloadSize(t *testing.T) {
	st := &enginev1alpha1.ShadowTest{}
	if got := siphonMaxPayloadSize(st); got != defaultSiphonMaxPayloadSize {
		t.Fatalf("default: got %d want %d", got, defaultSiphonMaxPayloadSize)
	}
	st.Spec.Siphon = &enginev1alpha1.SiphonSpec{MaxPayloadSize: 8192}
	if got := siphonMaxPayloadSize(st); got != 8192 {
		t.Fatalf("override: got %d want 8192", got)
	}
}

func TestFormatCaptureTargets_sortedAndStable(t *testing.T) {
	labels := map[string]string{"app": "api", "version": "v2", "env": "prod"}
	first := formatCaptureTargets(labels)
	second := formatCaptureTargets(labels)
	if len(first) != 3 {
		t.Fatalf("len %d", len(first))
	}
	want := []string{"app=api", "env=prod", "version=v2"}
	for i, w := range want {
		if first[i] != w {
			t.Fatalf("[%d] got %q want %q", i, first[i], w)
		}
	}
	for i := range first {
		if first[i] != second[i] {
			t.Fatalf("unstable order: %v vs %v", first, second)
		}
	}
	if got := formatCaptureTargets(nil); got != nil {
		t.Fatalf("nil map: got %v", got)
	}
}

func TestBuildPixieStreamRuleSpec(t *testing.T) {
	st := &enginev1alpha1.ShadowTest{}
	st.Namespace = "default"
	st.Name = "my-st"
	st.Spec.TargetNamespace = "prod"
	st.Spec.ServicePort = 8080
	st.Spec.Inputs = []enginev1alpha1.InputSpec{{Port: 80, Driver: "http_request"}}
	st.Spec.Siphon = &enginev1alpha1.SiphonSpec{
		MaxPayloadSize: 4096,
		ExcludePaths:   []string{`^/healthz$`},
	}

	dep := &appsv1.Deployment{
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "api", "tier": "web"}},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Ports: []corev1.ContainerPort{{ContainerPort: 80}},
					}},
				},
			},
		},
	}

	spec := buildPixieStreamRuleSpec(st, "shadow-default-my-st", dep)
	if spec.ShadowTestRef != "default/my-st" {
		t.Fatalf("ref %q", spec.ShadowTestRef)
	}
	if !spec.Active {
		t.Fatal("expected active")
	}
	if spec.TargetNamespace != "prod" {
		t.Fatalf("namespace %q", spec.TargetNamespace)
	}
	if spec.TargetLabels["app"] != "api" || spec.TargetLabels["tier"] != "web" {
		t.Fatalf("labels %v", spec.TargetLabels)
	}
	if spec.OTelEndpoint != shadowSiphonOTelEndpoint("shadow-default-my-st") {
		t.Fatalf("endpoint %q", spec.OTelEndpoint)
	}
	if spec.RecorderOTelEndpoint != "" {
		t.Fatalf("recorder endpoint %q", spec.RecorderOTelEndpoint)
	}
	if spec.MaxPayloadSize != 4096 {
		t.Fatalf("max payload %d", spec.MaxPayloadSize)
	}
	if len(spec.ExcludePaths) != 1 || spec.ExcludePaths[0] != `^/healthz$` {
		t.Fatalf("exclude %v", spec.ExcludePaths)
	}
	if len(spec.TargetPorts) == 0 {
		t.Fatal("expected default ingress port")
	}
}

func TestBuildPixieStreamRuleSpecEgressOnly(t *testing.T) {
	st := &enginev1alpha1.ShadowTest{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "egress-st"},
		Spec: enginev1alpha1.ShadowTestSpec{
			TargetNamespace: "prod",
			RecordAndReplay: []enginev1alpha1.RecordAndReplayHostSpec{
				{Host: "egress-httpbin.default.svc.cluster.local"},
			},
		},
	}
	dep := &appsv1.Deployment{
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "api"}},
			},
		},
	}
	spec := buildPixieStreamRuleSpec(st, "shadow-default-egress-st", dep)
	if spec.OTelEndpoint != "" {
		t.Fatalf("ingress endpoint %q", spec.OTelEndpoint)
	}
	want := shadowRecorderOTelEndpoint(st, "shadow-default-egress-st")
	if spec.RecorderOTelEndpoint != want {
		t.Fatalf("recorder endpoint %q want %q", spec.RecorderOTelEndpoint, want)
	}
	if len(spec.RecordAndReplayHosts) != 1 || spec.RecordAndReplayHosts[0] != "egress-httpbin.default.svc.cluster.local" {
		t.Fatalf("recordAndReplayHosts %v", spec.RecordAndReplayHosts)
	}
}

func TestBuildPixieStreamRuleSpecEgressHostPort(t *testing.T) {
	st := &enginev1alpha1.ShadowTest{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "egress-st"},
		Spec: enginev1alpha1.ShadowTestSpec{
			RecordAndReplay: []enginev1alpha1.RecordAndReplayHostSpec{
				{Host: "user-service.prod:8080"},
			},
		},
	}
	dep := &appsv1.Deployment{}
	spec := buildPixieStreamRuleSpec(st, "shadow-default-egress-st", dep)
	if len(spec.RecordAndReplayHosts) != 1 || spec.RecordAndReplayHosts[0] != "user-service.prod" {
		t.Fatalf("recordAndReplayHosts %v", spec.RecordAndReplayHosts)
	}
}

func TestTargetNamespaceFor_defaultsToCRNamespace(t *testing.T) {
	st := &enginev1alpha1.ShadowTest{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "st"},
	}
	if got := targetNamespaceFor(st); got != "default" {
		t.Fatalf("got %q want default", got)
	}
	st.Spec.TargetNamespace = "prod"
	if got := targetNamespaceFor(st); got != "prod" {
		t.Fatalf("got %q want prod", got)
	}
}

func TestEnsureShadowSiphonServicePatch_selector(t *testing.T) {
	svc := &corev1.Service{}
	patch := func() error {
		svc.Spec.Type = corev1.ServiceTypeClusterIP
		svc.Spec.Selector = map[string]string{
			"app.kubernetes.io/name": shadowSiphonServiceName,
		}
		svc.Spec.Ports = []corev1.ServicePort{{
			Name:     "otlp-grpc",
			Port:     shadowSiphonOTLPPort,
			Protocol: corev1.ProtocolTCP,
		}}
		return nil
	}
	if err := patch(); err != nil {
		t.Fatal(err)
	}
	if svc.Spec.Selector["app.kubernetes.io/name"] != shadowSiphonServiceName {
		t.Fatalf("selector = %v", svc.Spec.Selector)
	}
}

func boolPtr(v bool) *bool { return &v }

func TestSiphonEnabled(t *testing.T) {
	dep := &appsv1.Deployment{
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Ports: []corev1.ContainerPort{{ContainerPort: 80}},
					}},
				},
			},
		},
	}

	st := &enginev1alpha1.ShadowTest{}
	if siphonEnabled(st, dep) {
		t.Fatal("expected disabled with no inputs or explicit enable")
	}

	st.Spec.Siphon = &enginev1alpha1.SiphonSpec{Enabled: boolPtr(false)}
	if siphonEnabled(st, dep) {
		t.Fatal("explicit false should disable")
	}

	st.Spec.Siphon = &enginev1alpha1.SiphonSpec{Enabled: boolPtr(true)}
	if !siphonEnabled(st, dep) {
		t.Fatal("explicit true should enable")
	}

	st = &enginev1alpha1.ShadowTest{
		Spec: enginev1alpha1.ShadowTestSpec{
			RecordAndReplay: []enginev1alpha1.RecordAndReplayHostSpec{{Host: "example.com"}},
		},
	}
	if siphonEnabled(st, dep) {
		t.Fatal("recordAndReplay alone should not enable siphon")
	}

	st = &enginev1alpha1.ShadowTest{
		Spec: enginev1alpha1.ShadowTestSpec{
			Inputs: []enginev1alpha1.InputSpec{{Port: 80, Driver: "http_request"}},
		},
	}
	if !siphonEnabled(st, dep) {
		t.Fatal("matching ingress port should enable siphon")
	}

	st.Spec.Inputs = []enginev1alpha1.InputSpec{{Port: 9999, Driver: "http_request"}}
	if siphonEnabled(st, dep) {
		t.Fatal("non-matching port should not enable siphon")
	}

	st = &enginev1alpha1.ShadowTest{
		Spec: enginev1alpha1.ShadowTestSpec{
			RecordAndReplay: []enginev1alpha1.RecordAndReplayHostSpec{{Host: "example.com"}},
			Siphon:          &enginev1alpha1.SiphonSpec{Enabled: boolPtr(false)},
		},
	}
	if siphonEnabled(st, dep) {
		t.Fatal("explicit false should override recordAndReplay")
	}
}
