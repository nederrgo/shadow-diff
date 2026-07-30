package controller

import (
	"context"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	enginev1alpha1 "github.com/shadow-diff/monarch/api/v1alpha1"
)

func TestPodTerminalReason_crashLoopAndInit(t *testing.T) {
	t.Parallel()
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "shop-xyz"},
		Status: corev1.PodStatus{
			InitContainerStatuses: []corev1.ContainerStatus{{
				Name: "init",
				State: corev1.ContainerState{
					Waiting: &corev1.ContainerStateWaiting{
						Reason:  "CrashLoopBackOff",
						Message: "back-off",
					},
				},
			}},
		},
	}
	reason := podTerminalReason(pod, "Shop")
	if !reason.terminal {
		t.Fatal("expected terminal for init CrashLoopBackOff")
	}
	if !strings.Contains(reason.message, "Shop") || !strings.Contains(reason.message, "CrashLoopBackOff") {
		t.Fatalf("message = %q", reason.message)
	}
}

func TestDeploymentProgressTerminalReason(t *testing.T) {
	t.Parallel()
	deploy := &appsv1.Deployment{
		Status: appsv1.DeploymentStatus{
			Conditions: []appsv1.DeploymentCondition{{
				Type:    appsv1.DeploymentProgressing,
				Status:  corev1.ConditionTrue,
				Reason:  "ProgressDeadlineExceeded",
				Message: "progress deadline exceeded",
			}},
		},
	}
	reason := deploymentProgressTerminalReason(deploy, "Igris")
	if !reason.terminal {
		t.Fatal("expected terminal ProgressDeadlineExceeded")
	}
}

func TestDeploymentBootReady_timeout(t *testing.T) {
	t.Parallel()
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	_ = enginev1alpha1.AddToScheme(scheme)

	old := metav1.NewTime(time.Now().Add(-2 * time.Minute))
	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "shop",
			Namespace:         "shadow-default-boot-to",
			CreationTimestamp: old,
		},
		Status: appsv1.DeploymentStatus{AvailableReplicas: 0},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(deploy).Build()
	rec := &ShadowTestReconciler{Client: c, Scheme: scheme}

	ready, reason, err := rec.deploymentBootReady(context.Background(), "shadow-default-boot-to", "shop", "Shop")
	if err != nil {
		t.Fatal(err)
	}
	if ready {
		t.Fatal("expected not ready")
	}
	if !reason.terminal || !strings.Contains(reason.message, "did not become ready") {
		t.Fatalf("reason = %+v", reason)
	}
}

func TestMarkBootFailed_tearsDownKaiselAndNS(t *testing.T) {
	scheme := deleteLifecycleScheme(t)
	st := &enginev1alpha1.ShadowTest{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "boot-fail",
			Namespace:  "default",
			UID:        "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb",
			Finalizers: []string{finalizerName, s3Finalizer},
		},
		Spec: enginev1alpha1.ShadowTestSpec{
			TargetDeployment: "target-app",
			Storage: &enginev1alpha1.StorageConfig{
				Type:       "s3",
				BucketName: "b",
			},
		},
		Status: enginev1alpha1.ShadowTestStatus{
			Phase:           "Progressing",
			ShadowNamespace: "shadow-default-boot-fail",
		},
	}
	shadowNS := shadowNamespaceForCR(st)
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: shadowNS}}
	rule := &enginev1alpha1.KaiselRule{
		ObjectMeta: metav1.ObjectMeta{
			Name:      kaiselRuleName(st),
			Namespace: st.Namespace,
		},
		Spec: enginev1alpha1.KaiselRuleSpec{TargetIPs: []string{"10.0.0.1"}},
	}

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&enginev1alpha1.ShadowTest{}, &enginev1alpha1.KaiselRule{}).
		WithObjects(st.DeepCopy(), ns, rule).
		Build()
	rec := &ShadowTestReconciler{Client: c, Scheme: scheme}

	var live enginev1alpha1.ShadowTest
	if err := c.Get(context.Background(), types.NamespacedName{Name: st.Name, Namespace: st.Namespace}, &live); err != nil {
		t.Fatal(err)
	}
	res, err := rec.markBootFailed(context.Background(), &live, shadowNS, "Shop pod x: CrashLoopBackOff (boom)")
	if err != nil {
		t.Fatal(err)
	}
	if res.RequeueAfter == 0 {
		t.Fatal("expected requeue while namespace terminates")
	}

	if err := c.Get(context.Background(), types.NamespacedName{Name: st.Name, Namespace: st.Namespace}, &live); err != nil {
		t.Fatal(err)
	}
	if live.Status.Phase != phaseFailed {
		t.Fatalf("phase = %q", live.Status.Phase)
	}
	if !strings.Contains(live.Status.Message, "CrashLoopBackOff") {
		t.Fatalf("message = %q", live.Status.Message)
	}
	if len(live.Finalizers) < 2 {
		t.Fatalf("finalizers should remain, got %v", live.Finalizers)
	}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(rule), &enginev1alpha1.KaiselRule{}); !apierrors.IsNotFound(err) {
		t.Fatalf("KaiselRule should be deleted: %v", err)
	}
}

func TestReconcileStickyFailed_doesNotRecreateNamespace(t *testing.T) {
	scheme := deleteLifecycleScheme(t)
	st := &enginev1alpha1.ShadowTest{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "sticky-fail",
			Namespace:  "default",
			UID:        "cccccccc-cccc-cccc-cccc-cccccccccccc",
			Finalizers: []string{finalizerName, s3Finalizer},
		},
		Spec: enginev1alpha1.ShadowTestSpec{
			TargetDeployment: "target-app",
			OldImage:         "busybox:1.36",
			NewImage:         "busybox:1.36",
			Storage: &enginev1alpha1.StorageConfig{
				Type:       "s3",
				BucketName: "b",
			},
		},
		Status: enginev1alpha1.ShadowTestStatus{
			Phase:           phaseFailed,
			Message:         "Shop pod x: CrashLoopBackOff (boom)",
			ShadowNamespace: "shadow-default-sticky-fail",
			KaiselPhase:     "Disabled",
		},
	}
	shadowNS := shadowNamespaceForCR(st)

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&enginev1alpha1.ShadowTest{}).
		WithObjects(st.DeepCopy()).
		Build()
	rec := &ShadowTestReconciler{Client: c, Scheme: scheme}

	res, err := rec.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: st.Name, Namespace: st.Namespace},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Requeue || res.RequeueAfter > 0 {
		t.Fatalf("sticky Failed with NS gone should not requeue: %+v", res)
	}
	var ns corev1.Namespace
	if err := c.Get(context.Background(), types.NamespacedName{Name: shadowNS}, &ns); !apierrors.IsNotFound(err) {
		t.Fatalf("must not recreate shadow NS: %v", err)
	}
	var live enginev1alpha1.ShadowTest
	if err := c.Get(context.Background(), types.NamespacedName{Name: st.Name, Namespace: st.Namespace}, &live); err != nil {
		t.Fatal(err)
	}
	if live.Status.Phase != phaseFailed {
		t.Fatalf("phase = %q want Failed", live.Status.Phase)
	}
}
