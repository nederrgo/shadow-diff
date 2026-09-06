package controller

import (
	"context"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"

	enginev1alpha1 "github.com/shadow-diff/monarch/api/v1alpha1"
)

const bootRequeueInterval = 5 * time.Second

// bootGate is one ordered checkpoint in the ShadowTest boot sequence. A gate
// reconciles its resources, observes their readiness, and records that result
// in the topology status. Non-Deployment phases with richer state transitions
// (Kaisel, AMQP binding and replay triggering) remain explicit in their mode
// drivers.
type bootGate struct {
	name         string
	step         enginev1alpha1.BootStep
	waitMsg      string
	skip         bool
	reconcile    func(context.Context) error
	ready        func(context.Context) (bool, workloadWaitReason, error)
	record       func(*enginev1alpha1.ComponentStatus, bool)
	progress     []statusMutator
	requeueAfter time.Duration
}

// runBootGates advances through gates until all pass or one must wait/fail.
// done is true only when every non-skipped gate is ready.
func (r *ShadowTestReconciler) runBootGates(
	ctx context.Context,
	st *enginev1alpha1.ShadowTest,
	shadowNS string,
	boot *enginev1alpha1.ComponentStatus,
	gates []bootGate,
) (done bool, result ctrl.Result, err error) {
	for _, gate := range gates {
		if gate.skip {
			continue
		}
		if gate.reconcile != nil {
			if err := gate.reconcile(ctx); err != nil {
				_ = r.patchBootStatus(ctx, st, phaseFailed, err.Error(), shadowNS, gate.step, *boot)
				return false, ctrl.Result{}, err
			}
		}

		ready := true
		reason := workloadWaitReason{}
		if gate.ready != nil {
			var err error
			ready, reason, err = gate.ready(ctx)
			if err != nil {
				return false, ctrl.Result{}, err
			}
		}
		if gate.record != nil {
			gate.record(boot, ready)
		}
		if ready {
			continue
		}
		if reason.terminal {
			result, err := r.markBootFailed(ctx, st, shadowNS, reason.message, *boot)
			return false, result, err
		}

		waitMsg := gate.waitMsg
		if waitMsg == "" {
			waitMsg = "waiting for " + gate.name
		}
		progress := []statusMutator{
			statusBase(st.Generation, phaseProgressing, waitMsg, shadowNS),
			statusBoot(gate.step, *boot),
		}
		progress = append(progress, gate.progress...)
		_ = r.patchStatusCore(ctx, st, progress...)
		requeueAfter := gate.requeueAfter
		if requeueAfter <= 0 {
			requeueAfter = bootRequeueInterval
		}
		return false, ctrl.Result{RequeueAfter: requeueAfter}, nil
	}
	return true, ctrl.Result{}, nil
}

func (r *ShadowTestReconciler) localBeruBootGate(
	st *enginev1alpha1.ShadowTest,
	shadowNS string,
) bootGate {
	return bootGate{
		name:    "beru-local",
		step:    enginev1alpha1.BootStepProvisioningSinks,
		waitMsg: "Local analytics backend is booting up (beru-local)",
		reconcile: func(ctx context.Context) error {
			return r.reconcileLocalBeru(ctx, st, shadowNS)
		},
		ready: func(ctx context.Context) (bool, workloadWaitReason, error) {
			return r.localBeruReady(ctx, shadowNS)
		},
		record: func(boot *enginev1alpha1.ComponentStatus, ready bool) {
			boot.BeruReady = ready
		},
	}
}

// replayBootGates declares the replay ordering contract in one place:
// dependencies, Shop, ingress sinks/relay, then the three shadow roles.
func (r *ShadowTestReconciler) replayBootGates(
	st *enginev1alpha1.ShadowTest,
	shadowNS string,
	env []corev1.EnvVar,
	target *appsv1.Deployment,
) []bootGate {
	gates := []bootGate{
		{
			name:    "shadow dependencies",
			step:    enginev1alpha1.BootStepProvisioningSinks,
			waitMsg: "waiting for shadow dependencies",
			skip:    len(st.Spec.Dependencies) == 0,
			reconcile: func(ctx context.Context) error {
				return r.reconcileShadowDependencies(ctx, st, shadowNS)
			},
			ready: func(ctx context.Context) (bool, workloadWaitReason, error) {
				return r.shadowDependenciesReady(ctx, st, shadowNS)
			},
		},
		{
			name:    "Shop",
			step:    enginev1alpha1.BootStepProvisioningSinks,
			waitMsg: "waiting for Shop",
			reconcile: func(ctx context.Context) error {
				return r.reconcileShop(ctx, st, shadowNS)
			},
			ready: func(ctx context.Context) (bool, workloadWaitReason, error) {
				return r.shopDeploymentReady(ctx, shadowNS)
			},
			record: func(boot *enginev1alpha1.ComponentStatus, ready bool) {
				boot.ShopReady = ready
			},
		},
	}

	gates = append(gates, r.replayIngressBootGates(st, shadowNS)...)

	var roles map[string]bool
	gates = append(gates, bootGate{
		name:    "shadow Deployments",
		step:    enginev1alpha1.BootStepProvisioningShadow,
		waitMsg: "waiting for shadow Deployments",
		reconcile: func(ctx context.Context) error {
			return r.reconcileShadowWorkloadResources(ctx, st, shadowNS, env, target)
		},
		ready: func(ctx context.Context) (bool, workloadWaitReason, error) {
			var reason workloadWaitReason
			var err error
			roles, reason, err = r.shadowDeploymentsReady(ctx, st, shadowNS)
			return allRolesReady(roles), reason, err
		},
		record: func(boot *enginev1alpha1.ComponentStatus, _ bool) {
			boot.ShadowRolesReady = roles
		},
	})

	return gates
}

func (r *ShadowTestReconciler) replayIngressBootGates(
	st *enginev1alpha1.ShadowTest,
	shadowNS string,
) []bootGate {
	wantRelay := needsEgressRelayRabbitMQ(st)
	var gates []bootGate
	if needsAMQPIngress(st) {
		gates = append(gates, bootGate{
			name:    "igris-rabbitmq",
			step:    enginev1alpha1.BootStepProvisioningSinks,
			waitMsg: "waiting for igris-rabbitmq",
			reconcile: func(ctx context.Context) error {
				return r.reconcileIgrisRabbitMQStack(ctx, st, shadowNS)
			},
			ready: func(ctx context.Context) (bool, workloadWaitReason, error) {
				return r.igrisRabbitMQDeploymentReady(ctx, st, shadowNS)
			},
			record: recordIgrisUnlessRelay(wantRelay),
			progress: []statusMutator{
				statusExtras(nil, "", "", phaseProgressing),
			},
		})
	} else {
		gates = append(gates, bootGate{
			name:    "Igris",
			step:    enginev1alpha1.BootStepProvisioningSinks,
			waitMsg: "waiting for Igris",
			reconcile: func(ctx context.Context) error {
				return r.reconcileIgrisStack(ctx, st, shadowNS)
			},
			ready: func(ctx context.Context) (bool, workloadWaitReason, error) {
				return r.igrisDeploymentReady(ctx, st, shadowNS)
			},
			record: recordIgrisUnlessRelay(wantRelay),
		})
	}
	if wantRelay {
		relayGate := bootGate{
			name:    "egress-relay-rabbitmq",
			step:    enginev1alpha1.BootStepProvisioningSinks,
			waitMsg: "waiting for egress-relay-rabbitmq",
			reconcile: func(ctx context.Context) error {
				return r.reconcileEgressRelayRabbitMQStack(ctx, st, shadowNS)
			},
			ready: func(ctx context.Context) (bool, workloadWaitReason, error) {
				return r.egressRelayRabbitMQDeploymentReady(ctx, st, shadowNS)
			},
			record: func(boot *enginev1alpha1.ComponentStatus, ready bool) {
				boot.IgrisReady = ready
			},
		}
		if needsAMQPIngress(st) {
			relayGate.progress = []statusMutator{statusExtras(nil, "", "", phaseProgressing)}
		}
		gates = append(gates, relayGate)
	}
	return gates
}

func recordIgrisUnlessRelay(wantRelay bool) func(*enginev1alpha1.ComponentStatus, bool) {
	if wantRelay {
		return nil
	}
	return func(boot *enginev1alpha1.ComponentStatus, ready bool) {
		boot.IgrisReady = ready
	}
}

func (r *ShadowTestReconciler) reconcileIgrisStack(
	ctx context.Context,
	st *enginev1alpha1.ShadowTest,
	shadowNS string,
) error {
	if err := r.reconcileIgrisConfigMap(ctx, st, shadowNS); err != nil {
		return err
	}
	if err := r.reconcileIgrisDeployment(ctx, st, shadowNS); err != nil {
		return err
	}
	return r.reconcileIgrisService(ctx, st, shadowNS)
}
