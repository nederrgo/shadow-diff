package controller

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	enginev1alpha1 "github.com/shadow-diff/monarch/api/v1alpha1"
)

func boolPtr(v bool) *bool { return &v }

func deploymentControllerRef(dep *appsv1.Deployment) metav1.OwnerReference {
	return metav1.OwnerReference{
		APIVersion:         "apps/v1",
		Kind:               "Deployment",
		Name:               dep.Name,
		UID:                dep.UID,
		Controller:         boolPtr(true),
		BlockOwnerDeletion: boolPtr(true),
	}
}

func replicaSetControllerRef(rs *appsv1.ReplicaSet) metav1.OwnerReference {
	return metav1.OwnerReference{
		APIVersion:         "apps/v1",
		Kind:               "ReplicaSet",
		Name:               rs.Name,
		UID:                rs.UID,
		Controller:         boolPtr(true),
		BlockOwnerDeletion: boolPtr(true),
	}
}

func sharedLabelDeploymentPair(t *testing.T) (
	target *appsv1.Deployment,
	foreign *appsv1.Deployment,
	targetRS *appsv1.ReplicaSet,
	foreignRS *appsv1.ReplicaSet,
	targetPod *corev1.Pod,
	foreignPod *corev1.Pod,
) {
	t.Helper()
	sharedLabels := map[string]string{"app": "api"}

	target = &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "target-app",
			Namespace: "default",
			UID:       "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa",
		},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{MatchLabels: sharedLabels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: sharedLabels},
			},
		},
	}
	foreign = &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "other-app",
			Namespace: "default",
			UID:       "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb",
		},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{MatchLabels: sharedLabels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: sharedLabels},
			},
		},
	}

	targetRS = &appsv1.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "target-app-abc123",
			Namespace:       "default",
			UID:             "cccccccc-cccc-cccc-cccc-cccccccccccc",
			OwnerReferences: []metav1.OwnerReference{deploymentControllerRef(target)},
		},
	}
	foreignRS = &appsv1.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "other-app-def456",
			Namespace:       "default",
			UID:             "dddddddd-dddd-dddd-dddd-dddddddddddd",
			OwnerReferences: []metav1.OwnerReference{deploymentControllerRef(foreign)},
		},
	}

	targetPod = &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "target-app-abc123-xyz",
			Namespace:       "default",
			Labels:          sharedLabels,
			OwnerReferences: []metav1.OwnerReference{replicaSetControllerRef(targetRS)},
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning, PodIP: "10.0.0.1"},
	}
	foreignPod = &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "other-app-def456-uvw",
			Namespace:       "default",
			Labels:          sharedLabels,
			OwnerReferences: []metav1.OwnerReference{replicaSetControllerRef(foreignRS)},
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning, PodIP: "10.0.0.2"},
	}
	return
}

func TestListPodsOwnedByDeployment_sharedLabels(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	_ = enginev1alpha1.AddToScheme(scheme)

	target, foreign, targetRS, foreignRS, targetPod, foreignPod := sharedLabelDeploymentPair(t)
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		target, foreign, targetRS, foreignRS, targetPod, foreignPod,
	).Build()
	rec := &ShadowTestReconciler{Client: c, Scheme: scheme}

	pods, err := rec.listPodsOwnedByDeployment(context.Background(), "default", target)
	if err != nil {
		t.Fatal(err)
	}
	if len(pods) != 1 {
		t.Fatalf("len(pods) = %d want 1", len(pods))
	}
	if pods[0].Name != targetPod.Name {
		t.Fatalf("pod = %q want %q", pods[0].Name, targetPod.Name)
	}

	ips := runningPodIPs(pods)
	if len(ips) != 1 || ips[0] != "10.0.0.1" {
		t.Fatalf("ips = %v want [10.0.0.1]", ips)
	}
}

func TestPodOwnedByDeployment(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	_ = enginev1alpha1.AddToScheme(scheme)

	target, _, targetRS, foreignRS, targetPod, foreignPod := sharedLabelDeploymentPair(t)
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		target, targetRS, foreignRS, targetPod, foreignPod,
	).Build()
	rec := &ShadowTestReconciler{Client: c, Scheme: scheme}

	owned, err := rec.podOwnedByDeployment(context.Background(), targetPod, target)
	if err != nil {
		t.Fatal(err)
	}
	if !owned {
		t.Fatal("expected target pod owned by target deployment")
	}

	owned, err = rec.podOwnedByDeployment(context.Background(), foreignPod, target)
	if err != nil {
		t.Fatal(err)
	}
	if owned {
		t.Fatal("foreign pod must not be owned by target deployment")
	}
}

func TestMapPodToShadowTests_ownershipOnly(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	_ = enginev1alpha1.AddToScheme(scheme)

	target, _, targetRS, foreignRS, targetPod, foreignPod := sharedLabelDeploymentPair(t)
	st := &enginev1alpha1.ShadowTest{
		ObjectMeta: metav1.ObjectMeta{Name: "my-shadow", Namespace: "default"},
		Spec: enginev1alpha1.ShadowTestSpec{
			TargetDeployment: target.Name,
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		st, target, targetRS, foreignRS, targetPod, foreignPod,
	).Build()
	rec := &ShadowTestReconciler{Client: c, Scheme: scheme}

	targetReqs := rec.mapPodToShadowTests(context.Background(), targetPod)
	if len(targetReqs) != 1 {
		t.Fatalf("target pod: len(reqs) = %d want 1", len(targetReqs))
	}
	wantNN := types.NamespacedName{Namespace: "default", Name: "my-shadow"}
	if targetReqs[0].NamespacedName != wantNN {
		t.Fatalf("req = %+v", targetReqs[0])
	}

	foreignReqs := rec.mapPodToShadowTests(context.Background(), foreignPod)
	if len(foreignReqs) != 0 {
		t.Fatalf("foreign pod: len(reqs) = %d want 0", len(foreignReqs))
	}
}

func TestReconcileKaiselRule_ownershipOnlyIPs(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	_ = enginev1alpha1.AddToScheme(scheme)

	target, foreign, targetRS, foreignRS, targetPod, foreignPod := sharedLabelDeploymentPair(t)
	st := &enginev1alpha1.ShadowTest{
		ObjectMeta: metav1.ObjectMeta{Name: "my-shadow", Namespace: "default"},
		Spec: enginev1alpha1.ShadowTestSpec{
			TargetDeployment: target.Name,
		},
	}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&enginev1alpha1.KaiselRule{}).
		WithObjects(st, target, foreign, targetRS, foreignRS, targetPod, foreignPod).
		Build()
	rec := &ShadowTestReconciler{Client: c, Scheme: scheme}

	if err := rec.reconcileKaiselRule(context.Background(), st, shadowNamespaceForCR(st), target); err != nil {
		t.Fatal(err)
	}

	var rule enginev1alpha1.KaiselRule
	if err := c.Get(context.Background(), kaiselRuleKey(st), &rule); err != nil {
		t.Fatal(err)
	}
	if len(rule.Spec.TargetIPs) != 1 || rule.Spec.TargetIPs[0] != "10.0.0.1" {
		t.Fatalf("TargetIPs = %v want [10.0.0.1]", rule.Spec.TargetIPs)
	}
}
