package controller

import (
	"context"
	"fmt"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	enginev1alpha1 "github.com/shadow-diff/monarch/api/v1alpha1"
)

func recordOrderStorage() *enginev1alpha1.StorageConfig {
	return &enginev1alpha1.StorageConfig{
		Type:       "s3",
		BucketName: "shadow-diff-local",
		Endpoint:   "http://minio:9000",
		Region:     "us-east-1",
		CredentialsSecretRef: &corev1.LocalObjectReference{
			Name: "shadow-diff-s3",
		},
	}
}

func recordOrderShadowTest(name string) *enginev1alpha1.ShadowTest {
	st := &enginev1alpha1.ShadowTest{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "default",
			UID:       "11111111-2222-3333-4444-555555555555",
		},
		Spec: enginev1alpha1.ShadowTestSpec{
			TargetDeployment: "target-app",
			TargetNamespace:  "default",
			OldImage:         "busybox:1.36",
			NewImage:         "busybox:1.36",
			ServicePort:      8080,
			ApplicationPort:  8081,
			Mode:             modeRecord,
			Storage:          recordOrderStorage(),
			Inputs: []enginev1alpha1.InputSpec{{
				Driver: "http_request",
				Port:   8081,
			}},
		},
	}
	controllerutil.AddFinalizer(st, finalizerName)
	controllerutil.AddFinalizer(st, s3Finalizer)
	return st
}

func recordOrderTarget() *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "target-app", Namespace: "default"},
		Spec: appsv1.DeploymentSpec{
			Replicas: int32Ptr(1),
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "target-app"}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "target-app"}},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "app", Image: "busybox:1.36"}},
				},
			},
		},
	}
}

func recordOrderSecret() *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "shadow-diff-s3", Namespace: "default"},
		Type:       corev1.SecretTypeOpaque,
		Data: map[string][]byte{
			"AWS_ACCESS_KEY_ID":     []byte("minio"),
			"AWS_SECRET_ACCESS_KEY": []byte("minio123"),
		},
	}
}

func markAvailable(t *testing.T, c client.Client, ns, name string) {
	t.Helper()
	var deploy appsv1.Deployment
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: name}, &deploy); err != nil {
		t.Fatalf("get %s: %v", name, err)
	}
	deploy.Status.AvailableReplicas = 1
	deploy.Status.ReadyReplicas = 1
	deploy.Status.Replicas = 1
	if err := c.Status().Update(context.Background(), &deploy); err != nil {
		t.Fatalf("mark %s available: %v", name, err)
	}
}

// driveBeruLocalReady reconciles until beru-local exists, then marks it Available.
// Every ShadowTest provisions beru-local and the reconcile gates on its readiness,
// which a fake client never reports on its own.
func driveBeruLocalReady(
	t *testing.T,
	rec *ShadowTestReconciler,
	c client.Client,
	req reconcile.Request,
	shadowNS string,
) {
	t.Helper()
	for i := 0; i < 5; i++ {
		if _, err := rec.Reconcile(context.Background(), req); err != nil {
			t.Fatalf("reconcile beru bring-up %d: %v", i, err)
		}
		var beru appsv1.Deployment
		key := types.NamespacedName{Namespace: shadowNS, Name: localBeruName}
		if err := c.Get(context.Background(), key, &beru); err == nil {
			markAvailable(t, c, shadowNS, localBeruName)
			return
		}
	}
	t.Fatalf("beru-local was never created in %s", shadowNS)
}

// TestRecordMode_KaiselAfterSinksReady asserts KaiselRule is not created until
// Shop and Igris report AvailableReplicas > 0.
func TestRecordMode_KaiselAfterSinksReady(t *testing.T) {
	scheme := deleteLifecycleScheme(t)
	st := recordOrderShadowTest("record-order")
	shadowNS := shadowNamespaceForCR(st)

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(st.DeepCopy(), recordOrderTarget(), recordOrderSecret()).
		WithStatusSubresource(&enginev1alpha1.ShadowTest{}, &enginev1alpha1.KaiselRule{}, &appsv1.Deployment{}).
		Build()
	rec := &ShadowTestReconciler{Client: c, Scheme: scheme}
	nn := types.NamespacedName{Name: st.Name, Namespace: st.Namespace}
	req := reconcile.Request{NamespacedName: nn}

	driveBeruLocalReady(t, rec, c, req, shadowNS)
	if _, err := rec.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("reconcile after beru ready: %v", err)
	}

	// Shop + Igris should exist; KaiselRule must not.
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: shadowNS, Name: shopServiceName()}, &appsv1.Deployment{}); err != nil {
		t.Fatalf("shop should exist before KaiselRule: %v", err)
	}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: shadowNS, Name: igrisDeploymentName(st)}, &appsv1.Deployment{}); err != nil {
		t.Fatalf("igris should exist before KaiselRule: %v", err)
	}
	if err := c.Get(context.Background(), kaiselRuleKey(st), &enginev1alpha1.KaiselRule{}); !apierrors.IsNotFound(err) {
		t.Fatalf("KaiselRule must not exist before sinks Ready, err=%v", err)
	}

	// Still waiting on sinks → still no KaiselRule.
	if _, err := rec.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("reconcile waiting sinks: %v", err)
	}
	if err := c.Get(context.Background(), kaiselRuleKey(st), &enginev1alpha1.KaiselRule{}); !apierrors.IsNotFound(err) {
		t.Fatalf("KaiselRule must stay absent while sinks not Ready, err=%v", err)
	}

	markAvailable(t, c, shadowNS, shopServiceName())
	markAvailable(t, c, shadowNS, igrisDeploymentName(st))
	if _, err := rec.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("reconcile after sinks Ready: %v", err)
	}
	if err := c.Get(context.Background(), kaiselRuleKey(st), &enginev1alpha1.KaiselRule{}); err != nil {
		t.Fatalf("KaiselRule should exist after sinks Ready: %v", err)
	}

	var live enginev1alpha1.ShadowTest
	if err := c.Get(context.Background(), nn, &live); err != nil {
		t.Fatalf("get ShadowTest: %v", err)
	}
	if live.Status.Phase != "Ready" {
		t.Fatalf("phase=%q want Ready", live.Status.Phase)
	}
}

// TestRecordMode_AMQPBindAfterKaisel asserts declare happens in Phase 1 and
// bind only after KaiselRule exists.
func TestRecordMode_AMQPBindAfterKaisel(t *testing.T) {
	scheme := deleteLifecycleScheme(t)
	st := recordOrderShadowTest("record-amqp-order")
	st.Spec.Inputs = []enginev1alpha1.InputSpec{{
		Driver: "rabbitmq_message",
		AMQP: &enginev1alpha1.AMQPInputSpec{
			ProdURL:          "amqp://prod:5672",
			Exchange:         "orders",
			RoutingKey:       "k",
			TargetDependency: "rabbitmq",
		},
	}}
	st.Spec.Dependencies = []enginev1alpha1.DependencySpec{{
		Name:            "rabbitmq",
		Type:            "rabbitmq",
		Image:           "rabbitmq:3-management",
		EnvVarInjection: "AMQP_URL",
	}}
	shadowNS := shadowNamespaceForCR(st)

	var declareCalls, bindCalls int
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(st.DeepCopy(), recordOrderTarget(), recordOrderSecret()).
		WithStatusSubresource(&enginev1alpha1.ShadowTest{}, &enginev1alpha1.KaiselRule{}, &appsv1.Deployment{}).
		Build()
	rec := &ShadowTestReconciler{Client: c, Scheme: scheme}
	rec.ProdQueueEnsureDeclared = func(ctx context.Context, live *enginev1alpha1.ShadowTest) (string, error) {
		declareCalls++
		name := prodShadowQueueName(live)
		if live.Status.AmqpQueueName == "" {
			if err := rec.patchAmqpQueueName(ctx, live, name); err != nil {
				return "", err
			}
		}
		return name, nil
	}
	rec.ProdQueueEnsureBound = func(ctx context.Context, live *enginev1alpha1.ShadowTest) error {
		bindCalls++
		return nil
	}
	nn := types.NamespacedName{Name: st.Name, Namespace: st.Namespace}
	req := reconcile.Request{NamespacedName: nn}

	driveBeruLocalReady(t, rec, c, req, shadowNS)
	if _, err := rec.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("reconcile phase1: %v", err)
	}

	if declareCalls < 1 {
		t.Fatalf("declareCalls=%d want >=1 after Phase 1", declareCalls)
	}
	if bindCalls != 0 {
		t.Fatalf("bindCalls=%d want 0 before sinks Ready / KaiselRule", bindCalls)
	}
	if err := c.Get(context.Background(), kaiselRuleKey(st), &enginev1alpha1.KaiselRule{}); !apierrors.IsNotFound(err) {
		t.Fatalf("KaiselRule must be absent before sinks Ready, err=%v", err)
	}

	markAvailable(t, c, shadowNS, shopServiceName())
	markAvailable(t, c, shadowNS, igrisRabbitMQDeploymentName(st))
	bindsBefore := bindCalls
	if _, err := rec.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("reconcile after sinks Ready: %v", err)
	}
	if err := c.Get(context.Background(), kaiselRuleKey(st), &enginev1alpha1.KaiselRule{}); err != nil {
		t.Fatalf("KaiselRule after sinks Ready: %v", err)
	}
	if bindCalls <= bindsBefore {
		t.Fatalf("bindCalls=%d want > %d after KaiselRule", bindCalls, bindsBefore)
	}
}

func TestEnsureProdShadowQueue_DeclareThenBindHooks(t *testing.T) {
	scheme := deleteLifecycleScheme(t)
	st := recordOrderShadowTest("queue-wrapper")
	st.Spec.Inputs = []enginev1alpha1.InputSpec{{
		Driver: "rabbitmq_message",
		AMQP: &enginev1alpha1.AMQPInputSpec{
			ProdURL: "amqp://prod:5672", Exchange: "orders", RoutingKey: "k",
			TargetDependency: "rabbitmq",
		},
	}}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(st.DeepCopy()).
		WithStatusSubresource(&enginev1alpha1.ShadowTest{}).
		Build()

	var order []string
	rec := &ShadowTestReconciler{Client: c, Scheme: scheme}
	rec.ProdQueueEnsureDeclared = func(ctx context.Context, live *enginev1alpha1.ShadowTest) (string, error) {
		order = append(order, "declare")
		name := "shadow-diff-test"
		return name, rec.patchAmqpQueueName(ctx, live, name)
	}
	rec.ProdQueueEnsureBound = func(ctx context.Context, live *enginev1alpha1.ShadowTest) error {
		order = append(order, "bind")
		return nil
	}

	var live enginev1alpha1.ShadowTest
	if err := c.Get(context.Background(), types.NamespacedName{Name: st.Name, Namespace: st.Namespace}, &live); err != nil {
		t.Fatal(err)
	}
	if _, err := rec.ensureProdShadowQueue(context.Background(), &live); err != nil {
		t.Fatalf("ensureProdShadowQueue: %v", err)
	}
	if len(order) != 2 || order[0] != "declare" || order[1] != "bind" {
		t.Fatalf("order=%v want [declare bind]", order)
	}
}

func amqpRecordShadowTest(name string) *enginev1alpha1.ShadowTest {
	st := recordOrderShadowTest(name)
	st.Spec.Inputs = []enginev1alpha1.InputSpec{{
		Driver: "rabbitmq_message",
		AMQP: &enginev1alpha1.AMQPInputSpec{
			ProdURL:          "amqp://prod:5672",
			Exchange:         "orders",
			RoutingKey:       "k",
			TargetDependency: "rabbitmq",
		},
	}}
	st.Spec.Dependencies = []enginev1alpha1.DependencySpec{{
		Name:            "rabbitmq",
		Type:            "rabbitmq",
		Image:           "rabbitmq:3-management",
		EnvVarInjection: "AMQP_URL",
	}}
	return st
}

// TestRecordMode_QueueDeclareFail_MarkBootFailed asserts declare errors take the
// same autopsy path as deployment boot failure (Failed + teardown, finalizers kept).
func TestRecordMode_QueueDeclareFail_MarkBootFailed(t *testing.T) {
	scheme := deleteLifecycleScheme(t)
	st := amqpRecordShadowTest("amqp-declare-fail")
	shadowNS := shadowNamespaceForCR(st)

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(st.DeepCopy(), recordOrderTarget(), recordOrderSecret()).
		WithStatusSubresource(&enginev1alpha1.ShadowTest{}, &enginev1alpha1.KaiselRule{}, &appsv1.Deployment{}).
		Build()
	rec := &ShadowTestReconciler{Client: c, Scheme: scheme}
	rec.ProdQueueEnsureDeclared = func(ctx context.Context, live *enginev1alpha1.ShadowTest) (string, error) {
		return "", fmt.Errorf("queue declare %q: precondition failed", prodShadowQueueName(live))
	}
	nn := types.NamespacedName{Name: st.Name, Namespace: st.Namespace}
	req := reconcile.Request{NamespacedName: nn}

	driveBeruLocalReady(t, rec, c, req, shadowNS)
	if _, err := rec.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("reconcile declare fail: %v", err)
	}

	var live enginev1alpha1.ShadowTest
	if err := c.Get(context.Background(), nn, &live); err != nil {
		t.Fatal(err)
	}
	if live.Status.Phase != phaseFailed {
		t.Fatalf("phase=%q want Failed", live.Status.Phase)
	}
	if !strings.Contains(live.Status.Message, "queue declare") {
		t.Fatalf("message=%q want queue declare", live.Status.Message)
	}
	if len(live.Finalizers) < 2 {
		t.Fatalf("finalizers should remain, got %v", live.Finalizers)
	}
	if err := c.Get(context.Background(), kaiselRuleKey(st), &enginev1alpha1.KaiselRule{}); !apierrors.IsNotFound(err) {
		t.Fatalf("KaiselRule should be absent/deleted: %v", err)
	}
	var ns corev1.Namespace
	if err := c.Get(context.Background(), types.NamespacedName{Name: shadowNS}, &ns); err == nil {
		if ns.DeletionTimestamp == nil {
			t.Fatal("shadow NS should be deleted or terminating after declare fail")
		}
	} else if !apierrors.IsNotFound(err) {
		t.Fatalf("get shadow NS: %v", err)
	}
}

// TestRecordMode_QueueBindFail_MarkBootFailed asserts bind errors after KaiselRule
// tear down runtime the same way as deployment boot failure.
func TestRecordMode_QueueBindFail_MarkBootFailed(t *testing.T) {
	scheme := deleteLifecycleScheme(t)
	st := amqpRecordShadowTest("amqp-bind-fail")
	shadowNS := shadowNamespaceForCR(st)

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(st.DeepCopy(), recordOrderTarget(), recordOrderSecret()).
		WithStatusSubresource(&enginev1alpha1.ShadowTest{}, &enginev1alpha1.KaiselRule{}, &appsv1.Deployment{}).
		Build()
	rec := &ShadowTestReconciler{Client: c, Scheme: scheme}
	rec.ProdQueueEnsureDeclared = func(ctx context.Context, live *enginev1alpha1.ShadowTest) (string, error) {
		name := prodShadowQueueName(live)
		if live.Status.AmqpQueueName == "" {
			if err := rec.patchAmqpQueueName(ctx, live, name); err != nil {
				return "", err
			}
		}
		return name, nil
	}
	rec.ProdQueueEnsureBound = func(ctx context.Context, live *enginev1alpha1.ShadowTest) error {
		return fmt.Errorf("queue bind %q: NOT_FOUND - no queue", live.Status.AmqpQueueName)
	}
	nn := types.NamespacedName{Name: st.Name, Namespace: st.Namespace}
	req := reconcile.Request{NamespacedName: nn}

	driveBeruLocalReady(t, rec, c, req, shadowNS)
	if _, err := rec.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("reconcile phase1: %v", err)
	}
	markAvailable(t, c, shadowNS, shopServiceName())
	markAvailable(t, c, shadowNS, igrisRabbitMQDeploymentName(st))
	if _, err := rec.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("reconcile bind fail: %v", err)
	}

	var live enginev1alpha1.ShadowTest
	if err := c.Get(context.Background(), nn, &live); err != nil {
		t.Fatal(err)
	}
	if live.Status.Phase != phaseFailed {
		t.Fatalf("phase=%q want Failed", live.Status.Phase)
	}
	if !strings.Contains(live.Status.Message, "queue bind") {
		t.Fatalf("message=%q want queue bind", live.Status.Message)
	}
	if len(live.Finalizers) < 2 {
		t.Fatalf("finalizers should remain, got %v", live.Finalizers)
	}
	if err := c.Get(context.Background(), kaiselRuleKey(st), &enginev1alpha1.KaiselRule{}); !apierrors.IsNotFound(err) {
		t.Fatalf("KaiselRule should be torn down after bind fail: %v", err)
	}
	var ns corev1.Namespace
	if err := c.Get(context.Background(), types.NamespacedName{Name: shadowNS}, &ns); err == nil {
		if ns.DeletionTimestamp == nil {
			t.Fatal("shadow NS should be deleted or terminating after bind fail")
		}
	} else if !apierrors.IsNotFound(err) {
		t.Fatalf("get shadow NS: %v", err)
	}
}
