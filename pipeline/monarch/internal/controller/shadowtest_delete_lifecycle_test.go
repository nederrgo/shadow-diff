package controller

import (
	"context"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	enginev1alpha1 "github.com/shadow-diff/monarch/api/v1alpha1"
)

func deleteLifecycleScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("add core scheme: %v", err)
	}
	if err := enginev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add engine scheme: %v", err)
	}
	return scheme
}

func deletingShadowTest(name, ns string) *enginev1alpha1.ShadowTest {
	now := metav1.NewTime(time.Now())
	st := &enginev1alpha1.ShadowTest{
		ObjectMeta: metav1.ObjectMeta{
			Name:              name,
			Namespace:         ns,
			UID:               "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
			DeletionTimestamp: &now,
			Finalizers:        []string{finalizerName},
		},
		Spec: enginev1alpha1.ShadowTestSpec{
			TargetDeployment: "target-app",
			TargetNamespace:  ns,
			OldImage:         "busybox:1.36",
			NewImage:         "busybox:1.36",
			ServicePort:      8080,
			ApplicationPort:  80,
		},
		Status: enginev1alpha1.ShadowTestStatus{
			Phase:           "Progressing",
			Message:         "waiting for shadow dependencies",
			ShadowNamespace: "shadow-" + ns + "-" + name,
		},
	}
	return st
}

// TestReconcileDelete_LateCreatesAfterDeletionTimestampStillCleaned models the
// mid-bring-up race: an in-flight create lands Deployments / PixieStreamRule
// after deletionTimestamp is set. The next delete reconciles must still remove
// them and release the finalizer (no resume to Ready).
func TestReconcileDelete_LateCreatesAfterDeletionTimestampStillCleaned(t *testing.T) {
	scheme := deleteLifecycleScheme(t)
	st := deletingShadowTest("mid-delete", "default")
	shadowNS := shadowNamespaceForCR(st)

	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: shadowNS}}
	lateDeploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      shadowDeploymentName(st, roleCandidate),
			Namespace: shadowNS,
			Labels: map[string]string{
				labelManagedBy:      valueManagedBy,
				labelShadowTestName: st.Name,
				labelRole:           roleCandidate,
			},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: int32Ptr(1),
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "late"}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "late"}},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "busybox:1.36"}}},
			},
		},
	}
	lateRule := &enginev1alpha1.PixieStreamRule{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pixieStreamRuleName(st),
			Namespace: st.Namespace,
			Labels: map[string]string{
				labelManagedBy:      valueManagedBy,
				labelShadowTestName: st.Name,
			},
		},
		Spec: enginev1alpha1.PixieStreamRuleSpec{
			ShadowTestRef:   st.Namespace + "/" + st.Name,
			Active:          true,
			TargetNamespace: st.Namespace,
			ShadowNamespace: shadowNS,
		},
	}

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(st.DeepCopy(), ns, lateDeploy, lateRule).
		WithStatusSubresource(&enginev1alpha1.ShadowTest{}, &enginev1alpha1.PixieStreamRule{}).
		Build()
	rec := &ShadowTestReconciler{Client: c, Scheme: scheme}
	nn := types.NamespacedName{Name: st.Name, Namespace: st.Namespace}

	// Pass 1: deactivate+delete PixieStreamRule, delete shadow namespace.
	if _, err := rec.Reconcile(context.Background(), reconcile.Request{NamespacedName: nn}); err != nil {
		t.Fatalf("reconcile delete (pass 1): %v", err)
	}

	if err := c.Get(context.Background(), pixieStreamRuleKey(st), &enginev1alpha1.PixieStreamRule{}); !apierrors.IsNotFound(err) {
		t.Fatalf("PixieStreamRule still present after delete reconcile: %v", err)
	}
	if err := c.Get(context.Background(), types.NamespacedName{Name: shadowNS}, &corev1.Namespace{}); !apierrors.IsNotFound(err) {
		t.Fatalf("shadow namespace still present after delete reconcile: %v", err)
	}

	var after enginev1alpha1.ShadowTest
	if err := c.Get(context.Background(), nn, &after); err != nil {
		t.Fatalf("ShadowTest should still exist until finalizer is removed: %v", err)
	}
	if after.Status.Phase == "Ready" {
		t.Fatalf("bring-up resumed to Ready after deletionTimestamp; phase=%q msg=%q", after.Status.Phase, after.Status.Message)
	}
	if !controllerutil.ContainsFinalizer(&after, finalizerName) {
		t.Fatal("expected finalizer to remain until namespace is confirmed gone")
	}

	// Pass 2: namespace already gone → remove finalizer (API then deletes the CR).
	if _, err := rec.Reconcile(context.Background(), reconcile.Request{NamespacedName: nn}); err != nil {
		t.Fatalf("reconcile delete (pass 2): %v", err)
	}
	if err := c.Get(context.Background(), nn, &after); !apierrors.IsNotFound(err) {
		t.Fatalf("expected ShadowTest gone after finalizer removal, got: %v", err)
	}

	// Delete path must not recreate the namespace.
	if err := c.Get(context.Background(), types.NamespacedName{Name: shadowNS}, &corev1.Namespace{}); !apierrors.IsNotFound(err) {
		t.Fatalf("shadow namespace was recreated during delete: %v", err)
	}
}

// TestReconcileDelete_DoesNotRecreateShadowNamespaceWhileDeleting ensures that
// once deletionTimestamp is set, Reconcile stays on reconcileDelete and will
// not call the bring-up path that creates a shadow namespace.
func TestReconcileDelete_DoesNotRecreateShadowNamespaceWhileDeleting(t *testing.T) {
	scheme := deleteLifecycleScheme(t)
	st := deletingShadowTest("no-recreate", "default")
	shadowNS := shadowNamespaceForCR(st)

	// No shadow namespace object — early delete / already wiped.
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(st.DeepCopy()).
		WithStatusSubresource(&enginev1alpha1.ShadowTest{}).
		Build()
	rec := &ShadowTestReconciler{Client: c, Scheme: scheme}
	nn := types.NamespacedName{Name: st.Name, Namespace: st.Namespace}

	res, err := rec.Reconcile(context.Background(), reconcile.Request{NamespacedName: nn})
	if err != nil {
		t.Fatalf("reconcile delete: %v", err)
	}
	if res.Requeue || res.RequeueAfter > 0 {
		t.Fatalf("unexpected requeue after finalizer removal: %+v", res)
	}

	var after enginev1alpha1.ShadowTest
	if err := c.Get(context.Background(), nn, &after); !apierrors.IsNotFound(err) {
		t.Fatalf("expected ShadowTest gone after finalizer removal, got: %v", err)
	}
	if err := c.Get(context.Background(), types.NamespacedName{Name: shadowNS}, &corev1.Namespace{}); !apierrors.IsNotFound(err) {
		t.Fatalf("delete path must not recreate shadow namespace: %v", err)
	}
}

// TestReconcile_SkipsBringUpWhenDeletionTimestampSet is a guard against the
// mid-delete bug where Reconcile ignores deletionTimestamp and continues
// provisioning (e.g. ensuring the shadow namespace).
func TestReconcile_SkipsBringUpWhenDeletionTimestampSet(t *testing.T) {
	scheme := deleteLifecycleScheme(t)
	st := deletingShadowTest("skip-bringup", "default")
	shadowNS := shadowNamespaceForCR(st)

	target := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "target-app", Namespace: "default"},
		Spec: appsv1.DeploymentSpec{
			Replicas: int32Ptr(1),
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "target-app"}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "target-app"}},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "busybox:1.36"}}},
			},
		},
	}

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(st.DeepCopy(), target).
		WithStatusSubresource(&enginev1alpha1.ShadowTest{}).
		Build()
	rec := &ShadowTestReconciler{Client: c, Scheme: scheme}
	nn := types.NamespacedName{Name: st.Name, Namespace: st.Namespace}

	if _, err := rec.Reconcile(context.Background(), reconcile.Request{NamespacedName: nn}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	// Bring-up would create the shadow namespace; delete path must not.
	if err := c.Get(context.Background(), types.NamespacedName{Name: shadowNS}, &corev1.Namespace{}); !apierrors.IsNotFound(err) {
		t.Fatalf("expected no shadow namespace create while deleting: %v", err)
	}

	var after enginev1alpha1.ShadowTest
	if err := c.Get(context.Background(), nn, &after); !apierrors.IsNotFound(err) {
		t.Fatalf("expected ShadowTest gone via delete path, got: %v", err)
	}
}

func TestReconcileDelete_RemovesFinalizerOnlyAfterNamespaceGone(t *testing.T) {
	scheme := deleteLifecycleScheme(t)
	st := deletingShadowTest("wait-ns", "default")
	shadowNS := shadowNamespaceForCR(st)
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: shadowNS}}

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(st.DeepCopy(), ns).
		WithStatusSubresource(&enginev1alpha1.ShadowTest{}).
		Build()
	rec := &ShadowTestReconciler{Client: c, Scheme: scheme}
	nn := types.NamespacedName{Name: st.Name, Namespace: st.Namespace}

	res, err := rec.Reconcile(context.Background(), reconcile.Request{NamespacedName: nn})
	if err != nil {
		t.Fatalf("reconcile delete with namespace present: %v", err)
	}
	if res.RequeueAfter <= 0 {
		t.Fatalf("expected requeue while waiting for namespace deletion, got %+v", res)
	}

	var after enginev1alpha1.ShadowTest
	if err := c.Get(context.Background(), nn, &after); err != nil {
		t.Fatalf("get ShadowTest: %v", err)
	}
	if !controllerutil.ContainsFinalizer(&after, finalizerName) {
		t.Fatal("finalizer must remain until shadow namespace is NotFound")
	}

	// Fake client Delete removes the Namespace object immediately on pass 1.
	if err := c.Get(context.Background(), types.NamespacedName{Name: shadowNS}, &corev1.Namespace{}); !apierrors.IsNotFound(err) {
		t.Fatalf("expected namespace deleted on first delete reconcile: %v", err)
	}

	res, err = rec.Reconcile(context.Background(), reconcile.Request{NamespacedName: nn})
	if err != nil {
		t.Fatalf("reconcile delete after namespace gone: %v", err)
	}
	if res.Requeue || res.RequeueAfter > 0 {
		t.Fatalf("unexpected requeue after finalizer removal: %+v", res)
	}
	if err := c.Get(context.Background(), nn, &after); !apierrors.IsNotFound(err) {
		t.Fatalf("expected ShadowTest gone after finalizer removal, got: %v", err)
	}
}
