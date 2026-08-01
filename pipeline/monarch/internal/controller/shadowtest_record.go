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
// components carries the readiness observed during this pass back to Reconcile.
type recordModeResult struct {
	captureTargets []string
	kaiselPhase    string
	components     enginev1alpha1.ComponentStatus
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
	boot enginev1alpha1.ComponentStatus,
) recordModeResult {
	log := log.FromContext(ctx)

	// Phase 1: sinks + unbound queue (taps closed).
	if needsAMQPIngress(st) {
		if _, err := r.ensureProdShadowQueueDeclared(ctx, st); err != nil {
			res, ferr := r.markBootFailed(ctx, st, shadowNS, err.Error(), boot)
			return recordModeResult{components: boot, requeue: res, err: ferr}
		}
	}

	if err := r.reconcileShop(ctx, st, shadowNS); err != nil {
		_ = r.patchBootStatus(ctx, st, phaseFailed, err.Error(), shadowNS,
			enginev1alpha1.BootStepProvisioningSinks, boot)
		return recordModeResult{components: boot, err: err}
	}

	if err := r.reconcileRecordIngressSinks(ctx, st, shadowNS); err != nil {
		_ = r.patchBootStatus(ctx, st, phaseFailed, err.Error(), shadowNS,
			enginev1alpha1.BootStepProvisioningSinks, boot)
		return recordModeResult{components: boot, err: err}
	}

	shopReady, reason, err := r.shopDeploymentReady(ctx, shadowNS)
	if err != nil {
		return recordModeResult{components: boot, err: err}
	}
	boot.ShopReady = shopReady
	if !shopReady {
		if reason.terminal {
			res, ferr := r.markBootFailed(ctx, st, shadowNS, reason.message, boot)
			return recordModeResult{components: boot, requeue: res, err: ferr}
		}
		_ = r.patchBootStatus(ctx, st, phaseProgressing, "waiting for Shop", shadowNS,
			enginev1alpha1.BootStepProvisioningSinks, boot)
		return recordModeResult{components: boot, requeue: ctrl.Result{RequeueAfter: recordSinkRequeue}}
	}

	ingressReady, reason, err := r.recordIngressSinksReady(ctx, st, shadowNS)
	if err != nil {
		return recordModeResult{components: boot, err: err}
	}
	boot.IgrisReady = ingressReady
	if !ingressReady {
		if reason.terminal {
			res, ferr := r.markBootFailed(ctx, st, shadowNS, reason.message, boot)
			return recordModeResult{components: boot, requeue: res, err: ferr}
		}
		_ = r.patchBootStatus(ctx, st, phaseProgressing, "waiting for Igris", shadowNS,
			enginev1alpha1.BootStepProvisioningSinks, boot)
		return recordModeResult{components: boot, requeue: ctrl.Result{RequeueAfter: recordSinkRequeue}}
	}

	// Phase 2: open eBPF tap once sinks are Available.
	captureTargets, kaiselPhase, err := r.reconcileKaiselCapture(ctx, st, shadowNS, target)
	boot.KaiselRuleActive = err == nil && kaiselPhase == "Ready"
	if err != nil {
		log.Error(err, "Kaisel capture reconcile failed")
		kaiselPhase = "Degraded"
		_ = r.patchBootStatus(ctx, st, phaseProgressing,
			fmt.Sprintf("waiting for KaiselRule: %v", err), shadowNS,
			enginev1alpha1.BootStepActivatingEgressTap, boot)
		return recordModeResult{
			captureTargets: captureTargets,
			kaiselPhase:    kaiselPhase,
			components:     boot,
			requeue:        ctrl.Result{RequeueAfter: recordSinkRequeue},
		}
	}

	// Phase 3: bind AMQP only after KaiselRule exists.
	if needsAMQPIngress(st) {
		if err := r.ensureProdShadowQueueBound(ctx, st); err != nil {
			res, ferr := r.markBootFailed(ctx, st, shadowNS, err.Error(), boot)
			return recordModeResult{
				captureTargets: captureTargets,
				kaiselPhase:    kaiselPhase,
				components:     boot,
				requeue:        res,
				err:            ferr,
			}
		}
		boot.AMQPBound = true
	}

	return recordModeResult{
		captureTargets: captureTargets,
		kaiselPhase:    kaiselPhase,
		components:     boot,
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
