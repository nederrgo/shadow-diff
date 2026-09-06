package controller

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	enginev1alpha1 "github.com/shadow-diff/monarch/api/v1alpha1"
)

func TestReconcileShadowDeployment_EnvoyReadinessProbe(t *testing.T) {
	scheme := deleteLifecycleScheme(t)
	st := &enginev1alpha1.ShadowTest{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "probe-test",
			Namespace: "default",
			UID:       "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa",
		},
		Spec: enginev1alpha1.ShadowTestSpec{
			Mode:        modeReplay,
			NewImage:    "busybox:1.36",
			ServicePort: 3000,
			Storage: &enginev1alpha1.StorageConfig{
				Type:       "s3",
				BucketName: "b",
			},
		},
	}
	shadowNS := shadowNamespaceForCR(st)
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(st.DeepCopy(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: shadowNS}}).
		Build()
	rec := &ShadowTestReconciler{Client: c, Scheme: scheme}

	if err := rec.reconcileEnvoyConfigMap(context.Background(), st, shadowNS, roleControlA); err != nil {
		t.Fatalf("envoy configmap: %v", err)
	}
	if err := rec.reconcileShadowDeployment(context.Background(), st, shadowNS, roleControlA, "busybox:1.36", nil); err != nil {
		t.Fatalf("reconcileShadowDeployment: %v", err)
	}

	var deploy appsv1.Deployment
	name := sanitizeForDNS(st.Name + "-" + roleControlA)
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: shadowNS, Name: name}, &deploy); err != nil {
		t.Fatalf("get deploy: %v", err)
	}
	var envoy *corev1.Container
	for i := range deploy.Spec.Template.Spec.Containers {
		if deploy.Spec.Template.Spec.Containers[i].Name == containerEnvoySidecar {
			envoy = &deploy.Spec.Template.Spec.Containers[i]
			break
		}
	}
	if envoy == nil {
		t.Fatal("envoy-sidecar container missing")
	}
	if envoy.ReadinessProbe == nil || envoy.ReadinessProbe.TCPSocket == nil {
		t.Fatal("envoy-sidecar missing TCP readinessProbe")
	}
	if got := int32(envoy.ReadinessProbe.TCPSocket.Port.IntValue()); got != 3000 {
		t.Fatalf("envoy readiness port = %d, want servicePort 3000", got)
	}
}

func TestReconcileShadowDeployment_SoldierReadinessWhenProxied(t *testing.T) {
	scheme := deleteLifecycleScheme(t)
	st := &enginev1alpha1.ShadowTest{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "probe-soldier",
			Namespace: "default",
			UID:       "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb",
		},
		Spec: enginev1alpha1.ShadowTestSpec{
			Mode:     modeReplay,
			NewImage: "busybox:1.36",
			Storage: &enginev1alpha1.StorageConfig{
				Type:       "s3",
				BucketName: "b",
			},
			Dependencies: []enginev1alpha1.DependencySpec{{
				Name: "mongo", Type: "mongodb", EnvVarInjection: "MONGO_URL",
			}},
		},
	}
	shadowNS := shadowNamespaceForCR(st)
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(st.DeepCopy(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: shadowNS}}).
		Build()
	rec := &ShadowTestReconciler{Client: c, Scheme: scheme}

	if err := rec.reconcileEnvoyConfigMap(context.Background(), st, shadowNS, roleControlB); err != nil {
		t.Fatalf("envoy configmap: %v", err)
	}
	if err := rec.reconcileShadowDeployment(context.Background(), st, shadowNS, roleControlB, "busybox:1.36", nil); err != nil {
		t.Fatalf("reconcileShadowDeployment: %v", err)
	}

	var deploy appsv1.Deployment
	name := sanitizeForDNS(st.Name + "-" + roleControlB)
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: shadowNS, Name: name}, &deploy); err != nil {
		t.Fatalf("get deploy: %v", err)
	}
	var soldier *corev1.Container
	for i := range deploy.Spec.Template.Spec.Containers {
		if deploy.Spec.Template.Spec.Containers[i].Name == containerShadowSoldier {
			soldier = &deploy.Spec.Template.Spec.Containers[i]
			break
		}
	}
	if soldier == nil {
		t.Fatal("shadow-soldier missing with mongodb dep")
	}
	if soldier.ReadinessProbe == nil || soldier.ReadinessProbe.HTTPGet == nil {
		t.Fatal("shadow-soldier missing HTTP readinessProbe")
	}
	if got := int32(soldier.ReadinessProbe.HTTPGet.Port.IntValue()); got != soldierHealthPort {
		t.Fatalf("soldier readiness port = %d, want %d", got, soldierHealthPort)
	}
	if soldier.ReadinessProbe.HTTPGet.Path != soldierHealthPath {
		t.Fatalf("soldier readiness path = %q", soldier.ReadinessProbe.HTTPGet.Path)
	}
}
