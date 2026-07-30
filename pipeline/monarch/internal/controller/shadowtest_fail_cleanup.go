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

const phaseFailed = "Failed"

// markBootFailed patches phase=Failed, emits a Warning Event, then tears down
// KaiselRule / prod AMQP queue / shadow namespace (same steps as reconcileDelete,
// without removing finalizers or running S3 cleanup).
func (r *ShadowTestReconciler) markBootFailed(
	ctx context.Context,
	st *enginev1alpha1.ShadowTest,
	shadowNS, message string,
) (ctrl.Result, error) {
	_ = r.patchStatusFull(ctx, st, phaseFailed, message, shadowNS, nil, "Disabled", "", "")
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
	return r.finishBootFailedCleanup(ctx, st, shadowNS)
}
