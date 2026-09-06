package controller

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	enginev1alpha1 "github.com/shadow-diff/monarch/api/v1alpha1"
)

func TestReconcileDelete_S3RetentionDeleteCallsCleaner(t *testing.T) {
	scheme := deleteLifecycleScheme(t)
	now := metav1.NewTime(time.Now())
	st := &enginev1alpha1.ShadowTest{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "s3-del",
			Namespace:         "default",
			UID:               "cccccccc-cccc-cccc-cccc-cccccccccccc",
			DeletionTimestamp: &now,
			Finalizers:        []string{finalizerName, s3Finalizer},
		},
		Spec: enginev1alpha1.ShadowTestSpec{
			TargetDeployment: "target-app",
			NewImage:         "busybox:1.36",
			Storage: &enginev1alpha1.StorageConfig{
				Type:            "s3",
				BucketName:      "shadow-diff-local",
				RetentionPolicy: "Delete",
			},
		},
		Status: enginev1alpha1.ShadowTestStatus{
			ShadowNamespace: "shadow-default-s3-del",
		},
	}
	var calls atomic.Int32
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(st.DeepCopy()).
		WithStatusSubresource(&enginev1alpha1.ShadowTest{}).
		Build()
	rec := &ShadowTestReconciler{
		Client: c,
		Scheme: scheme,
		S3Cleaner: func(ctx context.Context, got *enginev1alpha1.ShadowTest) error {
			calls.Add(1)
			if got.Name != "s3-del" {
				t.Fatalf("unexpected name %q", got.Name)
			}
			return nil
		},
	}
	nn := types.NamespacedName{Name: st.Name, Namespace: st.Namespace}
	if _, err := rec.Reconcile(context.Background(), reconcile.Request{NamespacedName: nn}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("S3Cleaner calls = %d, want 1", calls.Load())
	}
	var after enginev1alpha1.ShadowTest
	if err := c.Get(context.Background(), nn, &after); !apierrors.IsNotFound(err) {
		t.Fatalf("expected CR gone after finalizers removed, got: %v", err)
	}
}

func TestReconcileDelete_S3RetentionRetainSkipsCleaner(t *testing.T) {
	scheme := deleteLifecycleScheme(t)
	now := metav1.NewTime(time.Now())
	st := &enginev1alpha1.ShadowTest{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "s3-retain",
			Namespace:         "default",
			UID:               "dddddddd-dddd-dddd-dddd-dddddddddddd",
			DeletionTimestamp: &now,
			Finalizers:        []string{finalizerName, s3Finalizer},
		},
		Spec: enginev1alpha1.ShadowTestSpec{
			TargetDeployment: "target-app",
			NewImage:         "busybox:1.36",
			Storage: &enginev1alpha1.StorageConfig{
				Type:            "s3",
				BucketName:      "shadow-diff-local",
				RetentionPolicy: "Retain",
			},
		},
		Status: enginev1alpha1.ShadowTestStatus{
			ShadowNamespace: "shadow-default-s3-retain",
		},
	}
	var calls atomic.Int32
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(st.DeepCopy()).
		WithStatusSubresource(&enginev1alpha1.ShadowTest{}).
		Build()
	rec := &ShadowTestReconciler{
		Client: c,
		Scheme: scheme,
		S3Cleaner: func(ctx context.Context, got *enginev1alpha1.ShadowTest) error {
			calls.Add(1)
			return nil
		},
	}
	nn := types.NamespacedName{Name: st.Name, Namespace: st.Namespace}
	if _, err := rec.Reconcile(context.Background(), reconcile.Request{NamespacedName: nn}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if calls.Load() != 0 {
		t.Fatalf("S3Cleaner should not run for Retain, calls=%d", calls.Load())
	}
	var after enginev1alpha1.ShadowTest
	if err := c.Get(context.Background(), nn, &after); !apierrors.IsNotFound(err) {
		t.Fatalf("expected CR gone, got: %v", err)
	}
}

func TestReplayAdminURL(t *testing.T) {
	t.Parallel()
	st := &enginev1alpha1.ShadowTest{ObjectMeta: metav1.ObjectMeta{Name: "demo"}}
	got := replayAdminURL(st, "shadow-default-demo")
	want := "http://demo-igris.shadow-default-demo.svc.cluster.local:9090/v1/replay/start"
	if got != want {
		t.Fatalf("replayAdminURL = %q, want %q", got, want)
	}
}

func TestReplayAdminURL_AMQPOnly(t *testing.T) {
	t.Parallel()
	st := &enginev1alpha1.ShadowTest{
		ObjectMeta: metav1.ObjectMeta{Name: "rmq"},
		Spec: enginev1alpha1.ShadowTestSpec{
			Inputs: []enginev1alpha1.InputSpec{{
				Driver: "rabbitmq_message",
				AMQP: &enginev1alpha1.AMQPInputSpec{
					ProdURL: "amqp://prod:5672", Exchange: "orders", RoutingKey: "k",
					TargetDependency:     "rabbitmq",
					CredentialsSecretRef: testAMQPCredentialsRef(),
				},
			}},
		},
	}
	got := replayAdminURL(st, "shadow-default-rmq")
	want := "http://rmq-igris-rabbitmq.shadow-default-rmq.svc.cluster.local:9090/v1/replay/start"
	if got != want {
		t.Fatalf("replayAdminURL AMQP = %q, want %q", got, want)
	}
}

func TestReplayWorkloadNames_AMQPOnly(t *testing.T) {
	t.Parallel()
	st := &enginev1alpha1.ShadowTest{
		ObjectMeta: metav1.ObjectMeta{Name: "rmq"},
		Spec: enginev1alpha1.ShadowTestSpec{
			Inputs: []enginev1alpha1.InputSpec{{
				Driver: "rabbitmq_message",
				AMQP: &enginev1alpha1.AMQPInputSpec{
					ProdURL: "amqp://prod:5672", Exchange: "orders", RoutingKey: "k",
					TargetDependency:     "rabbitmq",
					CredentialsSecretRef: testAMQPCredentialsRef(),
				},
			}},
		},
	}
	names := replayWorkloadNames(st)
	want := igrisRabbitMQDeploymentName(st)
	found := false
	for _, n := range names {
		if n == want {
			found = true
		}
		if n == igrisDeploymentName(st) {
			t.Fatalf("AMQP-only roll-ready list must not include HTTP igris %q: %v", n, names)
		}
	}
	if !found {
		t.Fatalf("missing %q in %v", want, names)
	}
}

func TestCleanupS3IfNeeded_NoFinalizer(t *testing.T) {
	t.Parallel()
	rec := &ShadowTestReconciler{}
	st := &enginev1alpha1.ShadowTest{}
	if err := rec.cleanupS3IfNeeded(context.Background(), st); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
}
