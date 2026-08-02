package controller

import (
	"context"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"

	enginev1alpha1 "github.com/shadow-diff/monarch/api/v1alpha1"
)

// Phase and capture-phase values are defined in api/v1alpha1 next to the status
// fields they populate, so the gRPC contract and the controller cannot drift.
const (
	phaseFailed      = enginev1alpha1.PhaseFailed
	phaseReady       = enginev1alpha1.PhaseReady
	phaseProgressing = enginev1alpha1.PhaseProgressing
	phaseDeleting    = enginev1alpha1.PhaseDeleting
	phaseDeleted     = enginev1alpha1.PhaseDeleted

	capturePhaseReady    = enginev1alpha1.CapturePhaseReady
	capturePhaseDegraded = enginev1alpha1.CapturePhaseDegraded
	capturePhaseDisabled = enginev1alpha1.CapturePhaseDisabled
)

// markBootFailed patches phase=Failed, emits a Warning Event, then tears down
// KaiselRule / prod AMQP queue / shadow namespace (same steps as reconcileDelete,
// without removing finalizers or running S3 cleanup). comp is the boot state observed
// at the moment of failure, kept on the CR for autopsy.
func (r *ShadowTestReconciler) markBootFailed(
	ctx context.Context,
	st *enginev1alpha1.ShadowTest,
	shadowNS, message string,
	comp enginev1alpha1.ComponentStatus,
) (ctrl.Result, error) {
	_ = r.patchStatusCore(ctx, st,
		statusBase(st.Generation, phaseFailed, message, shadowNS),
		statusExtras(nil, capturePhaseDisabled, "", ""),
		statusBoot(enginev1alpha1.BootStepFailed, comp),
	)
	if r.Recorder != nil {
		r.Recorder.Event(st, corev1.EventTypeWarning, "BootFailed", message)
	}
	return r.finishBootFailedCleanup(ctx, st, shadowNS)
}

// finishBootFailedCleanup removes capture + shadow runtime while keeping the CR.
// Requeues while the shadow namespace is still terminating.
func (r *ShadowTestReconciler) finishBootFailedCleanup(
	ctx context.Context,
	st *enginev1alpha1.ShadowTest,
	shadowNS string,
) (ctrl.Result, error) {
	if hasRabbitMQInput(st) {
		if err := r.deleteProdShadowQueue(ctx, st); err != nil {
			return ctrl.Result{RequeueAfter: 10 * time.Second}, err
		}
	}
	if err := r.deleteKaiselRule(ctx, st); err != nil {
		return ctrl.Result{RequeueAfter: 5 * time.Second}, err
	}

	var ns corev1.Namespace
	err := r.Get(ctx, types.NamespacedName{Name: shadowNS}, &ns)
	if apierrors.IsNotFound(err) {
		return ctrl.Result{}, nil
	}
	if err != nil {
		return ctrl.Result{}, err
	}
	if ns.DeletionTimestamp == nil {
		if err := r.Delete(ctx, &ns); err != nil && !apierrors.IsNotFound(err) {
			return ctrl.Result{}, err
		}
	}
	return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
}

// reconcileStickyFailed continues teardown if needed, otherwise no-ops so
// Monarch does not recreate the shadow stack.
func (r *ShadowTestReconciler) reconcileStickyFailed(
	ctx context.Context,
	st *enginev1alpha1.ShadowTest,
	shadowNS string,
) (ctrl.Result, error) {
	// Backfill bootStep for CRs that failed before this field existed; the DeepEqual
	// guard in patchStatusCore makes this a no-op once set. Components are preserved.
	_ = r.patchStatusCore(ctx, st, func(s *enginev1alpha1.ShadowTestStatus) {
		s.BootStep = enginev1alpha1.BootStepFailed
	})
	return r.finishBootFailedCleanup(ctx, st, shadowNS)
}
