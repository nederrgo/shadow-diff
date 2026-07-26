package controller

import (
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	enginev1alpha1 "github.com/shadow-diff/monarch/api/v1alpha1"
)

func TestSamplePercentage(t *testing.T) {
	st := &enginev1alpha1.ShadowTest{}
	if got := samplePercentage(st); got != defaultSamplePercentage {
		t.Fatalf("default: got %d want %d", got, defaultSamplePercentage)
	}
	st.Spec.SamplePercentage = 10
	if got := samplePercentage(st); got != 10 {
		t.Fatalf("override: got %d want 10", got)
	}
}

func TestKaiselIgrisBaseURL(t *testing.T) {
	st := &enginev1alpha1.ShadowTest{}
	st.Namespace = "default"
	st.Name = "bats-kaisel-capture"
	st.Spec.ServicePort = 8080

	got := kaiselIgrisBaseURL(st)
	want := "http://bats-kaisel-capture-igris.shadow-default-bats-kaisel-capture.svc.cluster.local:8080"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}

	st.Spec.ServicePort = 0
	got = kaiselIgrisBaseURL(st)
	want = "http://bats-kaisel-capture-igris.shadow-default-bats-kaisel-capture.svc.cluster.local:8888"
	if got != want {
		t.Fatalf("default port: got %q want %q", got, want)
	}
}

// Kaisel POSTs egress request/response pairs straight to the shadow
// namespace's Shop, which derives the mock key its own ext_proc later looks up.
func TestKaiselEgressBaseURL(t *testing.T) {
	got := kaiselEgressBaseURL("shadow-default-bats-kaisel-capture")
	want := "http://shop.shadow-default-bats-kaisel-capture.svc.cluster.local:8080"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	if !strings.HasPrefix(got, "http://") {
		t.Errorf("EgressBaseURL must carry a scheme; kaisel rejects one without: %q", got)
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
	st.Spec.SamplePercentage = 25

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
	if spec.OTelEndpoint != "" {
		t.Fatalf("expected empty OTelEndpoint (kaisel owns ingress), got %q", spec.OTelEndpoint)
	}
	want := shadowRecorderOTelEndpoint(st, "shadow-default-my-st")
	if spec.RecorderOTelEndpoint != want {
		t.Fatalf("recorder endpoint %q want %q", spec.RecorderOTelEndpoint, want)
	}
	if spec.MaxPayloadSize != defaultPixieMaxPayloadSize {
		t.Fatalf("max payload %d", spec.MaxPayloadSize)
	}
	if len(spec.ExcludePaths) != 0 {
		t.Fatalf("exclude %v", spec.ExcludePaths)
	}
	if spec.SamplePercentage != 25 {
		t.Fatalf("sample percentage %d", spec.SamplePercentage)
	}
}

func TestBuildPixieStreamRuleSpecAlwaysHasRecorderEndpoint(t *testing.T) {
	st := &enginev1alpha1.ShadowTest{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "egress-st"},
		Spec: enginev1alpha1.ShadowTestSpec{
			TargetNamespace: "prod",
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
		t.Fatalf("expected empty OTelEndpoint, got %q", spec.OTelEndpoint)
	}
	want := shadowRecorderOTelEndpoint(st, "shadow-default-egress-st")
	if spec.RecorderOTelEndpoint != want {
		t.Fatalf("recorder endpoint %q want %q", spec.RecorderOTelEndpoint, want)
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

func TestHTTPIngressCaptureEnabled(t *testing.T) {
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
	if !httpIngressCaptureEnabled(st, dep) {
		t.Fatal("default http_request input on servicePort should enable capture")
	}

	st = &enginev1alpha1.ShadowTest{
		Spec: enginev1alpha1.ShadowTestSpec{
			Inputs: []enginev1alpha1.InputSpec{{Port: 80, Driver: "http_request"}},
		},
	}
	if !httpIngressCaptureEnabled(st, dep) {
		t.Fatal("matching ingress port should enable capture")
	}

	st.Spec.Inputs = []enginev1alpha1.InputSpec{{Port: 9999, Driver: "http_request"}}
	if httpIngressCaptureEnabled(st, dep) {
		t.Fatal("non-matching port should not enable capture")
	}

	st = &enginev1alpha1.ShadowTest{
		Spec: enginev1alpha1.ShadowTestSpec{
			ServicePort:     8888,
			ApplicationPort: 8080,
			Inputs:          []enginev1alpha1.InputSpec{{Port: 8888, Driver: "http_request"}},
		},
	}
	depNoPorts := &appsv1.Deployment{
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "app"}},
				},
			},
		},
	}
	if !httpIngressCaptureEnabled(st, depNoPorts) {
		t.Fatal("http_request on servicePort should enable capture even without container ports")
	}
	ports := kaiselIngressPorts(st)
	if len(ports) != 1 || ports[0] != 8080 {
		t.Fatalf("expected application port 8080, got %v", ports)
	}
}

func TestInt32ToUint16Ports(t *testing.T) {
	got := int32ToUint16Ports([]int32{80, 8080, 0, 65536, 443})
	want := []uint16{80, 8080, 443}
	if len(got) != len(want) {
		t.Fatalf("len %d want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("[%d] got %d want %d", i, got[i], want[i])
		}
	}
}

func TestKaiselRuleIPSetsEqual(t *testing.T) {
	if !ipSetsEqual([]string{"10.0.0.1", "10.0.0.2"}, []string{"10.0.0.1", "10.0.0.2"}) {
		t.Error("identical sets should be equal")
	}
	if ipSetsEqual([]string{"10.0.0.1"}, []string{"10.0.0.2"}) {
		t.Error("different sets should not be equal")
	}
	if ipSetsEqual([]string{"10.0.0.1"}, []string{"10.0.0.1", "10.0.0.2"}) {
		t.Error("different lengths should not be equal")
	}
}

func TestKaiselRulePortSetsEqual(t *testing.T) {
	if !portSetsEqual([]uint16{80, 443}, []uint16{443, 80}) {
		t.Error("same ports in different order should be equal")
	}
	if portSetsEqual([]uint16{80}, []uint16{443}) {
		t.Error("different ports should not be equal")
	}
}
