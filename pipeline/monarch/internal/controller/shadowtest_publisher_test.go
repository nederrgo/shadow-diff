package controller

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	enginev1alpha1 "github.com/shadow-diff/monarch/api/v1alpha1"
)

func reconcileRequestFor(st *enginev1alpha1.ShadowTest) reconcile.Request {
	return reconcile.Request{
		NamespacedName: types.NamespacedName{Name: st.Name, Namespace: st.Namespace},
	}
}

// fakePublisher records every status broadcast.
type fakePublisher struct {
	phases []string
	steps  []enginev1alpha1.BootStep
}

func (f *fakePublisher) Publish(st *enginev1alpha1.ShadowTest) {
	f.phases = append(f.phases, st.Status.Phase)
	f.steps = append(f.steps, st.Status.BootStep)
}

// TestPublisher_FiresOnTransitionsAndNotOnceConverged pins the property the live
// stream depends on: exactly one publish per real status change, and none at all
// on a converged reconcile. Pairs with TestStatus_ConvergedReconcileDoesNotWrite.
func TestPublisher_FiresOnTransitionsAndNotOnceConverged(t *testing.T) {
	scheme := deleteLifecycleScheme(t)
	st := recordOrderShadowTest("publisher-test")
	shadowNS := shadowNamespaceForCR(st)

	pub := &fakePublisher{}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(st.DeepCopy(), recordOrderTarget(), recordOrderSecret()).
		WithStatusSubresource(&enginev1alpha1.ShadowTest{}, &enginev1alpha1.KaiselRule{}, &appsv1.Deployment{}).
		Build()
	rec := &ShadowTestReconciler{Client: c, Scheme: scheme, StatusPublisher: pub}
	req := reconcileRequestFor(st)

	driveBeruLocalReady(t, rec, c, req, shadowNS)
	if _, err := rec.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("reconcile after beru ready: %v", err)
	}
	markAvailable(t, c, shadowNS, shopServiceName())
	markAvailable(t, c, shadowNS, igrisDeploymentName(st))
	if _, err := rec.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("reconcile to Ready: %v", err)
	}

	if len(pub.phases) == 0 {
		t.Fatal("no status was ever published")
	}
	if last := pub.phases[len(pub.phases)-1]; last != phaseReady {
		t.Fatalf("last published phase = %q, want Ready", last)
	}
	if last := pub.steps[len(pub.steps)-1]; last != enginev1alpha1.BootStepReady {
		t.Fatalf("last published bootStep = %q, want Ready", last)
	}

	// Converged: the DeepEqual guard in patchStatusCore short-circuits before the
	// patch, so nothing new may be published.
	before := len(pub.phases)
	if _, err := rec.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("converged reconcile: %v", err)
	}
	if got := len(pub.phases); got != before {
		t.Fatalf("converged reconcile published %d extra update(s): %v", got-before, pub.phases[before:])
	}
}

// TestPublisher_DeleteEmitsDeletingThenDeleted pins the two stream events the
// UI depends on: Deleting while teardown runs, Deleted once finalizers drop.
func TestPublisher_DeleteEmitsDeletingThenDeleted(t *testing.T) {
	scheme := deleteLifecycleScheme(t)
	st := deletingShadowTest("publisher-delete", "default")
	// No shadow namespace → delete path finishes in one reconcile.
	pub := &fakePublisher{}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(st.DeepCopy()).
		WithStatusSubresource(&enginev1alpha1.ShadowTest{}).
		Build()
	rec := &ShadowTestReconciler{Client: c, Scheme: scheme, StatusPublisher: pub}

	if _, err := rec.Reconcile(context.Background(), reconcileRequestFor(st)); err != nil {
		t.Fatalf("reconcile delete: %v", err)
	}
	if len(pub.phases) < 2 {
		t.Fatalf("want Deleting then Deleted, got %v", pub.phases)
	}
	if pub.phases[0] != phaseDeleting {
		t.Fatalf("first publish = %q, want Deleting", pub.phases[0])
	}
	if last := pub.phases[len(pub.phases)-1]; last != phaseDeleted {
		t.Fatalf("last publish = %q, want Deleted", last)
	}
}

// A nil publisher must be a no-op, which is how every other test runs.
func TestPublisher_NilIsSafe(t *testing.T) {
	scheme := deleteLifecycleScheme(t)
	st := recordOrderShadowTest("publisher-nil")

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(st.DeepCopy(), recordOrderTarget(), recordOrderSecret()).
		WithStatusSubresource(&enginev1alpha1.ShadowTest{}, &enginev1alpha1.KaiselRule{}, &appsv1.Deployment{}).
		Build()
	rec := &ShadowTestReconciler{Client: c, Scheme: scheme}

	if _, err := rec.Reconcile(context.Background(), reconcileRequestFor(st)); err != nil {
		t.Fatalf("reconcile with nil publisher: %v", err)
	}
}
