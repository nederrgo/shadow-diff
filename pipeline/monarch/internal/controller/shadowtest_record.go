package controller

import (
	"context"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log"

	enginev1alpha1 "github.com/shadow-diff/monarch/api/v1alpha1"
)

const recordSinkRequeue = 2 * time.Second

// recordModeResult is returned by reconcileRecordMode before the shared Ready patch.
type recordModeResult struct {
	captureTargets []string
	kaiselPhase    string
	requeue        ctrl.Result
	done           bool // true when Ready path may continue
	err            error
}

// reconcileRecordMode runs bottom-up record phases:
// 1) unbound AMQP queue (if any) + Shop + Igris sinks, gate Ready
// 2) KaiselRule
// 3) AMQP QueueBind (if any)
func (r *ShadowTestReconciler) reconcileRecordMode(
	ctx context.Context,
	st *enginev1alpha1.ShadowTest,
	shadowNS string,
	target *appsv1.Deployment,
) recordModeResult {
	log := log.FromContext(ctx)

	// Phase 1: sinks + unbound queue (taps closed).
	if needsAMQPIngress(st) {
		if _, err := r.ensureProdShadowQueueDeclared(ctx, st); err != nil {
			res, ferr := r.markBootFailed(ctx, st, shadowNS, err.Error())
			return recordModeResult{requeue: res, err: ferr}
		}
	}

	if err := r.reconcileShop(ctx, st, shadowNS); err != nil {
		_ = r.patchStatus(ctx, st, phaseFailed, err.Error(), shadowNS)
		return recordModeResult{err: err}
	}

	if err := r.reconcileRecordIngressSinks(ctx, st, shadowNS); err != nil {
		_ = r.patchStatus(ctx, st, phaseFailed, err.Error(), shadowNS)
		return recordModeResult{err: err}
	}

	shopReady, reason, err := r.shopDeploymentReady(ctx, shadowNS)
	if err != nil {
		return recordModeResult{err: err}
	}
	if !shopReady {
		if reason.terminal {
			res, ferr := r.markBootFailed(ctx, st, shadowNS, reason.message)
			return recordModeResult{requeue: res, err: ferr}
		}
		_ = r.patchStatus(ctx, st, "Progressing", "waiting for Shop", shadowNS)
		return recordModeResult{requeue: ctrl.Result{RequeueAfter: recordSinkRequeue}}
	}

	ingressReady, reason, err := r.recordIngressSinksReady(ctx, st, shadowNS)
	if err != nil {
		return recordModeResult{err: err}
	}
	if !ingressReady {
		if reason.terminal {
			res, ferr := r.markBootFailed(ctx, st, shadowNS, reason.message)
			return recordModeResult{requeue: res, err: ferr}
		}
		_ = r.patchStatus(ctx, st, "Progressing", "waiting for Igris", shadowNS)
		return recordModeResult{requeue: ctrl.Result{RequeueAfter: recordSinkRequeue}}
	}

	// Phase 2: open eBPF tap once sinks are Available.
	captureTargets, kaiselPhase, err := r.reconcileKaiselCapture(ctx, st, shadowNS, target)
	if err != nil {
		log.Error(err, "Kaisel capture reconcile failed")
		kaiselPhase = "Degraded"
		_ = r.patchStatus(ctx, st, "Progressing",
			fmt.Sprintf("waiting for KaiselRule: %v", err), shadowNS)
		return recordModeResult{
			captureTargets: captureTargets,
			kaiselPhase:    kaiselPhase,
			requeue:        ctrl.Result{RequeueAfter: recordSinkRequeue},
		}
	}

	// Phase 3: bind AMQP only after KaiselRule exists.
	if needsAMQPIngress(st) {
		if err := r.ensureProdShadowQueueBound(ctx, st); err != nil {
			res, ferr := r.markBootFailed(ctx, st, shadowNS, err.Error())
			return recordModeResult{
				captureTargets: captureTargets,
				kaiselPhase:    kaiselPhase,
				requeue:        res,
				err:            ferr,
			}
		}
	}

	return recordModeResult{
		captureTargets: captureTargets,
		kaiselPhase:    kaiselPhase,
		done:           true,
	}
}

// reconcileRecordIngressSinks creates Igris / igris-rabbitmq without opening AMQP bind.
func (r *ShadowTestReconciler) reconcileRecordIngressSinks(
	ctx context.Context,
	st *enginev1alpha1.ShadowTest,
	shadowNS string,
) error {
	if needsAMQPIngress(st) {
		return r.reconcileIgrisRabbitMQStack(ctx, st, shadowNS)
	}
	if needsHTTPTCPIngress(st) {
		if err := r.reconcileIgrisConfigMap(ctx, st, shadowNS); err != nil {
			return err
		}
		if err := r.reconcileIgrisDeployment(ctx, st, shadowNS); err != nil {
			return err
		}
		if err := r.reconcileIgrisService(ctx, st, shadowNS); err != nil {
			return err
		}
	}
	return nil
}

func (r *ShadowTestReconciler) recordIngressSinksReady(
	ctx context.Context,
	st *enginev1alpha1.ShadowTest,
	shadowNS string,
) (bool, workloadWaitReason, error) {
	if needsAMQPIngress(st) {
		return r.igrisRabbitMQDeploymentReady(ctx, st, shadowNS)
	}
	if needsHTTPTCPIngress(st) {
		return r.igrisDeploymentReady(ctx, st, shadowNS)
	}
	return true, workloadWaitReason{}, nil
}
