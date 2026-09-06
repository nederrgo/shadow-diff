package controller

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	enginev1alpha1 "github.com/shadow-diff/monarch/api/v1alpha1"
)

func TestTargetContainerImage(t *testing.T) {
	target := &appsv1.Deployment{
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "app", Image: "nginx:1.25"}},
				},
			},
		},
	}
	got, err := targetContainerImage(target)
	if err != nil {
		t.Fatal(err)
	}
	if got != "nginx:1.25" {
		t.Fatalf("image = %q, want nginx:1.25", got)
	}

	empty := &appsv1.Deployment{}
	if _, err := targetContainerImage(empty); err == nil {
		t.Fatal("expected error for deployment with no containers")
	}
}

func TestEnsureOldImage_PinsWhenEmpty(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := enginev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := appsv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}

	st := &enginev1alpha1.ShadowTest{
		ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "default"},
		Spec: enginev1alpha1.ShadowTestSpec{
			TargetDeployment: "target-app",
			NewImage:         "candidate:v2",
		},
	}
	target := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "target-app", Namespace: "default"},
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "app", Image: "baseline:v1"}},
				},
			},
		},
	}

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(st).Build()
	r := &ShadowTestReconciler{Client: c}

	patched, err := r.ensureOldImage(context.Background(), st, target)
	if err != nil {
		t.Fatal(err)
	}
	if !patched {
		t.Fatal("expected spec patch when oldImage empty")
	}

	var got enginev1alpha1.ShadowTest
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(st), &got); err != nil {
		t.Fatal(err)
	}
	if got.Spec.OldImage != "baseline:v1" {
		t.Fatalf("spec.oldImage = %q, want baseline:v1", got.Spec.OldImage)
	}
}

func TestEnsureOldImage_SkipsWhenSet(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := enginev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}

	st := &enginev1alpha1.ShadowTest{
		ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "default"},
		Spec: enginev1alpha1.ShadowTestSpec{
			OldImage: "user-pinned:v1",
			NewImage: "candidate:v2",
		},
	}
	target := &appsv1.Deployment{
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "app", Image: "target:v9"}},
				},
			},
		},
	}

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(st).Build()
	r := &ShadowTestReconciler{Client: c}

	patched, err := r.ensureOldImage(context.Background(), st, target)
	if err != nil {
		t.Fatal(err)
	}
	if patched {
		t.Fatal("expected no patch when oldImage already set")
	}
	if st.Spec.OldImage != "user-pinned:v1" {
		t.Fatalf("spec.oldImage = %q, want user-pinned:v1", st.Spec.OldImage)
	}
}

func TestReconcileShadowWorkloads_RejectsEmptyOldImage(t *testing.T) {
	r := &ShadowTestReconciler{}
	st := &enginev1alpha1.ShadowTest{
		Spec: enginev1alpha1.ShadowTestSpec{NewImage: "candidate:v2"},
	}
	_, _, err := r.reconcileShadowWorkloads(context.Background(), st, "shadow-default-demo", nil, nil)
	if err == nil {
		t.Fatal("expected error when spec.oldImage empty")
	}
}
