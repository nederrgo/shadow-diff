package controller

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	enginev1alpha1 "github.com/shadow-diff/monarch/api/v1alpha1"
)

// statusTestReconciler wires a record-mode ShadowTest onto a fake client and returns
// the reconciler, the CR key and the shadow namespace.
func statusTestReconciler(t *testing.T, name string) (*ShadowTestReconciler, *enginev1alpha1.ShadowTest, reconcile.Request, string) {
	t.Helper()
	scheme := deleteLifecycleScheme(t)
	st := recordOrderShadowTest(name)
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(st.DeepCopy(), recordOrderTarget(), recordOrderSecret()).
		WithStatusSubresource(&enginev1alpha1.ShadowTest{}, &enginev1alpha1.KaiselRule{}, &appsv1.Deployment{}).
		Build()
	req := reconcile.Request{NamespacedName: types.NamespacedName{Name: st.Name, Namespace: st.Namespace}}
	return &ShadowTestReconciler{Client: c, Scheme: scheme}, st, req, shadowNamespaceForCR(st)
}

func getShadowTest(t *testing.T, rec *ShadowTestReconciler, req reconcile.Request) enginev1alpha1.ShadowTest {
	t.Helper()
	var live enginev1alpha1.ShadowTest
	if err := rec.Get(context.Background(), req.NamespacedName, &live); err != nil {
		t.Fatalf("get ShadowTest: %v", err)
	}
	return live
}

// TestStatus_MidBootReportsProvisioningSinks asserts the first pass, while beru-local
// is still coming up, reports the sinks step with no component ready.
func TestStatus_MidBootReportsProvisioningSinks(t *testing.T) {
	rec, _, req, _ := statusTestReconciler(t, "status-midboot")

	if _, err := rec.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	live := getShadowTest(t, rec, req)
	if live.Status.BootStep != enginev1alpha1.BootStepProvisioningSinks {
		t.Fatalf("bootStep = %q, want %q", live.Status.BootStep, enginev1alpha1.BootStepProvisioningSinks)
	}
	if live.Status.Phase != phaseProgressing {
		t.Fatalf("phase = %q, want Progressing", live.Status.Phase)
	}
	if live.Status.Components.BeruReady {
		t.Fatal("beruReady should be false while beru-local is booting")
	}
	if live.Status.Components.TargetDeployment != "target-app" {
		t.Fatalf("targetDeployment = %q", live.Status.Components.TargetDeployment)
	}
	if !meta.IsStatusConditionTrue(live.Status.Conditions, enginev1alpha1.ConditionProgressing) {
		t.Fatal("Progressing condition should be True mid-boot")
	}
	if meta.IsStatusConditionTrue(live.Status.Conditions, enginev1alpha1.ConditionReady) {
		t.Fatal("Ready condition should not be True mid-boot")
	}
}

// TestStatus_ReadyReportsAllComponents drives a record-mode ShadowTest to Ready and
// asserts the topology fields Tusk consumes.
func TestStatus_ReadyReportsAllComponents(t *testing.T) {
	rec, st, req, shadowNS := statusTestReconciler(t, "status-ready")

	driveBeruLocalReady(t, rec, rec.Client, req, shadowNS)
	if _, err := rec.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("reconcile after beru ready: %v", err)
	}
	markAvailable(t, rec.Client, shadowNS, shopServiceName())
	markAvailable(t, rec.Client, shadowNS, igrisDeploymentName(st))
	if _, err := rec.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("reconcile after sinks ready: %v", err)
	}

	live := getShadowTest(t, rec, req)
	if live.Status.BootStep != enginev1alpha1.BootStepReady {
		t.Fatalf("bootStep = %q, want Ready", live.Status.BootStep)
	}
	comp := live.Status.Components
	for label, ready := range map[string]bool{
		"beruReady":        comp.BeruReady,
		"shopReady":        comp.ShopReady,
		"igrisReady":       comp.IgrisReady,
		"kaiselRuleActive": comp.KaiselRuleActive,
		// No AMQP ingress on this fixture, so the queue is trivially bound.
		"amqpBound": comp.AMQPBound,
	} {
		if !ready {
			t.Errorf("components.%s = false, want true", label)
		}
	}
	// Record mode never provisions the shadow roles.
	if len(comp.ShadowRolesReady) != 0 {
		t.Errorf("shadowRolesReady = %v, want empty in record mode", comp.ShadowRolesReady)
	}
	if !meta.IsStatusConditionTrue(live.Status.Conditions, enginev1alpha1.ConditionReady) {
		t.Error("Ready condition should be True")
	}
	if meta.IsStatusConditionTrue(live.Status.Conditions, enginev1alpha1.ConditionDegraded) {
		t.Error("Degraded condition should be False")
	}
	ready := meta.FindStatusCondition(live.Status.Conditions, enginev1alpha1.ConditionReady)
	if ready.ObservedGeneration != live.Generation {
		t.Errorf("observedGeneration = %d, want %d", ready.ObservedGeneration, live.Generation)
	}
}

// TestStatus_ConvergedReconcileDoesNotWrite guards patchStatusCore's DeepEqual check:
// a converged ShadowTest must not be re-patched on every watch event.
func TestStatus_ConvergedReconcileDoesNotWrite(t *testing.T) {
	rec, st, req, shadowNS := statusTestReconciler(t, "status-noop")

	driveBeruLocalReady(t, rec, rec.Client, req, shadowNS)
	if _, err := rec.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("reconcile after beru ready: %v", err)
	}
	markAvailable(t, rec.Client, shadowNS, shopServiceName())
	markAvailable(t, rec.Client, shadowNS, igrisDeploymentName(st))
	if _, err := rec.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("reconcile to Ready: %v", err)
	}

	converged := getShadowTest(t, rec, req)
	if converged.Status.Phase != phaseReady {
		t.Fatalf("setup did not converge: phase = %q", converged.Status.Phase)
	}

	if _, err := rec.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("converged reconcile: %v", err)
	}

	after := getShadowTest(t, rec, req)
	if after.ResourceVersion != converged.ResourceVersion {
		t.Fatalf("converged reconcile rewrote status: resourceVersion %s -> %s",
			converged.ResourceVersion, after.ResourceVersion)
	}
}

// TestStatusBase_ConditionsAreStableAcrossPasses asserts SetStatusCondition does not
// churn LastTransitionTime when nothing changed, which the DeepEqual guard relies on.
func TestStatusBase_ConditionsAreStableAcrossPasses(t *testing.T) {
	var s enginev1alpha1.ShadowTestStatus
	apply := statusBase(3, phaseReady, "all good", "shadow-default-x")

	apply(&s)
	first := meta.FindStatusCondition(s.Conditions, enginev1alpha1.ConditionReady).LastTransitionTime
	before := s.DeepCopy()

	apply(&s)
	if got := meta.FindStatusCondition(s.Conditions, enginev1alpha1.ConditionReady).LastTransitionTime; !got.Equal(&first) {
		t.Fatalf("LastTransitionTime moved on an unchanged pass: %v -> %v", first, got)
	}
	if !equality.Semantic.DeepEqual(*before, s) {
		t.Fatal("re-applying the same status produced a diff")
	}

	// A phase flip must move the transition time.
	statusBase(3, phaseFailed, "boom", "shadow-default-x")(&s)
	if !meta.IsStatusConditionTrue(s.Conditions, enginev1alpha1.ConditionDegraded) {
		t.Fatal("Degraded should be True after a Failed phase")
	}
	if meta.FindStatusCondition(s.Conditions, enginev1alpha1.ConditionReady).Status != metav1.ConditionFalse {
		t.Fatal("Ready should be False after a Failed phase")
	}
}
