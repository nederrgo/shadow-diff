package controller

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	enginev1alpha1 "github.com/shadow-diff/monarch/api/v1alpha1"
)

func TestDeleteShadowRoleWorkloads(t *testing.T) {
	scheme := deleteLifecycleScheme(t)
	st := &enginev1alpha1.ShadowTest{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "gc-test",
			Namespace: "default",
			UID:       "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb",
		},
		Spec: enginev1alpha1.ShadowTestSpec{
			TargetDeployment: "target",
			Storage: &enginev1alpha1.StorageConfig{
				Type:       "s3",
				BucketName: "b",
			},
		},
	}
	shadowNS := "shadow-default-gc-test"
	roleDeploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      shadowDeploymentName(st, roleControlA),
			Namespace: shadowNS,
			Labels: map[string]string{
				labelManagedBy:     valueManagedBy,
				labelShadowTestUID: string(st.UID),
				labelRole:          roleControlA,
			},
		},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "a"}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "a"}},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "busybox"}}},
			},
		},
	}
	roleSvc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      shadowDeploymentName(st, roleControlA),
			Namespace: shadowNS,
			Labels: map[string]string{
				labelManagedBy:     valueManagedBy,
				labelShadowTestUID: string(st.UID),
				labelRole:          roleControlA,
			},
		},
		Spec: corev1.ServiceSpec{
			Ports:    []corev1.ServicePort{{Port: 80}},
			Selector: map[string]string{"app": "a"},
		},
	}
	shop := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      shopServiceName(),
			Namespace: shadowNS,
			Labels: map[string]string{
				labelManagedBy:     valueManagedBy,
				labelShadowTestUID: string(st.UID),
			},
		},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "shop"}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "shop"}},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "shop", Image: "shop"}}},
			},
		},
	}

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(roleDeploy, roleSvc, shop).Build()
	rec := &ShadowTestReconciler{Client: c, Scheme: scheme}
	if err := rec.deleteShadowRoleWorkloads(context.Background(), st, shadowNS); err != nil {
		t.Fatalf("deleteShadowRoleWorkloads: %v", err)
	}

	if err := c.Get(context.Background(), types.NamespacedName{Namespace: shadowNS, Name: roleDeploy.Name}, &appsv1.Deployment{}); !apierrors.IsNotFound(err) {
		t.Fatalf("role deploy still present: %v", err)
	}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: shadowNS, Name: roleSvc.Name}, &corev1.Service{}); !apierrors.IsNotFound(err) {
		t.Fatalf("role svc still present: %v", err)
	}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: shadowNS, Name: shop.Name}, &appsv1.Deployment{}); err != nil {
		t.Fatalf("shop should remain: %v", err)
	}
}
