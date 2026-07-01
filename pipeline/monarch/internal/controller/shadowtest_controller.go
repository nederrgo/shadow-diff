// +kubebuilder:rbac:groups="",resources=namespaces,verbs=create;delete;get;list;watch
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=engine.shadow-diff.io,resources=shadowtests,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=engine.shadow-diff.io,resources=shadowtests/finalizers,verbs=update
// +kubebuilder:rbac:groups=engine.shadow-diff.io,resources=shadowtests/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=engine.shadow-diff.io,resources=pixiestreamrules,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=engine.shadow-diff.io,resources=pixiestreamrules/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=engine.shadow-diff.io,resources=pixiestreamrules/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch

package controller

import (
	"context"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	enginev1alpha1 "github.com/shadow-diff/monarch/api/v1alpha1"
)

type ShadowTestReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

func (r *ShadowTestReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var shadowTest enginev1alpha1.ShadowTest
	if err := r.Get(ctx, req.NamespacedName, &shadowTest); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	shadowNS := shadowNamespaceForCR(&shadowTest)

	if !shadowTest.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, req.NamespacedName, shadowNS)
	}

	if !controllerutil.ContainsFinalizer(&shadowTest, finalizerName) {
		base := shadowTest.DeepCopy()
		controllerutil.AddFinalizer(&shadowTest, finalizerName)
		if err := r.Patch(ctx, &shadowTest, client.MergeFrom(base)); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	if err := validateInputs(&shadowTest); err != nil {
		_ = r.patchStatus(ctx, &shadowTest, "Failed", err.Error(), shadowNS)
		return ctrl.Result{}, nil
	}
	if err := validateDependencies(&shadowTest); err != nil {
		_ = r.patchStatus(ctx, &shadowTest, "Failed", err.Error(), shadowNS)
		return ctrl.Result{}, nil
	}

	var target appsv1.Deployment
	targetKey := types.NamespacedName{Namespace: targetNamespaceFor(&shadowTest), Name: shadowTest.Spec.TargetDeployment}
	if err := r.Get(ctx, targetKey, &target); err != nil {
		if apierrors.IsNotFound(err) {
			msg := fmt.Sprintf("target Deployment %s/%s not found", targetNamespaceFor(&shadowTest), shadowTest.Spec.TargetDeployment)
			log.Info(msg)
			_ = r.patchStatus(ctx, &shadowTest, "Failed", msg, shadowNS)
			return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
		}
		return ctrl.Result{}, err
	}

	if len(shadowTest.Spec.Inputs) == 0 && shadowTest.Spec.TargetDeployment != "" && !siphonEnabled(&shadowTest, &target) {
		log.Info("live capture inactive: spec.inputs is empty and Siphon is disabled",
			"level", "warn",
			"shadowtest", fmt.Sprintf("%s/%s", shadowTest.Namespace, shadowTest.Name))
	}

	if err := r.ensureShadowNamespace(ctx, &shadowTest, shadowNS); err != nil {
		return ctrl.Result{}, err
	}

	if err := r.reconcileLocalBeruIfNeeded(ctx, &shadowTest, shadowNS); err != nil {
		_ = r.patchStatus(ctx, &shadowTest, "Failed", err.Error(), shadowNS)
		return ctrl.Result{}, err
	}
	if usesLocalBeru(&shadowTest) {
		ready, reason, err := r.localBeruReady(ctx, shadowNS)
		if err != nil {
			return ctrl.Result{}, err
		}
		if !ready {
			if reason.terminal {
				_ = r.patchStatus(ctx, &shadowTest, "Failed", reason.message, shadowNS)
				return ctrl.Result{}, nil
			}
			_ = r.patchStatus(ctx, &shadowTest, "Progressing",
				"Local analytics backend is booting up (beru-local)", shadowNS)
			return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
		}
	}

	env, warnMsg := envFromTarget(&target)

	if err := r.reconcileShadowDependencies(ctx, &shadowTest, shadowNS); err != nil {
		_ = r.patchStatus(ctx, &shadowTest, "Failed", err.Error(), shadowNS)
		return ctrl.Result{}, err
	}
	if len(shadowTest.Spec.Dependencies) > 0 {
		depsReady, err := r.shadowDependenciesReady(ctx, &shadowTest, shadowNS)
		if err != nil {
			return ctrl.Result{}, err
		}
		if !depsReady {
			_ = r.patchStatus(ctx, &shadowTest, "Progressing", "waiting for shadow dependencies", shadowNS)
			return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
		}
	}

	ingressReady, err := r.reconcileIngressRelays(ctx, req, &shadowTest, shadowNS)
	if err != nil {
		_ = r.patchStatus(ctx, &shadowTest, "Failed", err.Error(), shadowNS)
		return ctrl.Result{}, err
	}
	if !ingressReady {
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}

	shadowsReady, err := r.reconcileShadowWorkloads(ctx, &shadowTest, shadowNS, env, &target)
	if err != nil {
		_ = r.patchStatus(ctx, &shadowTest, "Failed", err.Error(), shadowNS)
		return ctrl.Result{}, err
	}
	if !shadowsReady {
		_ = r.patchStatus(ctx, &shadowTest, "Progressing", "waiting for shadow Deployments", shadowNS)
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}

	if egressRecordingEnabled(&shadowTest) {
		if err := r.reconcileRecorderStack(ctx, &shadowTest, shadowNS); err != nil {
			_ = r.patchStatus(ctx, &shadowTest, "Failed", err.Error(), shadowNS)
			return ctrl.Result{}, err
		}
		recorderReady, err := r.recorderDeploymentReady(ctx, &shadowTest, shadowNS)
		if err != nil {
			return ctrl.Result{}, err
		}
		if !recorderReady {
			_ = r.patchStatus(ctx, &shadowTest, "Progressing", "waiting for Recorder", shadowNS)
			return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
		}
	}

	captureTargets, siphonPhase, err := r.reconcileSiphonCapture(ctx, &shadowTest, shadowNS, &target)
	if err != nil {
		log.Error(err, "Siphon capture reconcile failed")
		siphonPhase = "Degraded"
	}

	var igrisEndpoint string
	igrisRMQPhase := ""
	if needsAMQPIngress(&shadowTest) {
		igrisRMQPhase = "Ready"
		igrisEndpoint = fmt.Sprintf("amqp queue %s; igris-rabbitmq %s",
			shadowTest.Status.AmqpQueueName,
			shadowServiceHost(shadowNS, igrisRabbitMQServiceName(&shadowTest)))
	} else {
		igrisHost := shadowServiceHost(shadowNS, igrisServiceName(&shadowTest))
		igrisEndpoint = fmt.Sprintf("%s:%d", igrisHost, servicePortFor(&shadowTest))
	}

	msg := warnMsg
	if msg == "" {
		msg = fmt.Sprintf("shadow environment ready with ingress [%s]", listenersSummary(&shadowTest))
	} else {
		msg = fmt.Sprintf("%s; ingress [%s]", msg, listenersSummary(&shadowTest))
	}
	if siphonPhase != "" && siphonPhase != "Disabled" {
		msg = fmt.Sprintf("%s; Siphon %s", msg, siphonPhase)
	}

	if err := r.patchStatusFull(ctx, &shadowTest, "Ready", msg, shadowNS, captureTargets, siphonPhase, igrisEndpoint, igrisRMQPhase); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

func (r *ShadowTestReconciler) reconcileIngressRelays(
	ctx context.Context,
	req ctrl.Request,
	st *enginev1alpha1.ShadowTest,
	shadowNS string,
) (bool, error) {
	if needsAMQPIngress(st) {
		if _, err := r.ensureProdShadowQueue(ctx, st); err != nil {
			return false, err
		}
		if err := r.refreshShadowTest(ctx, req.NamespacedName, st); err != nil {
			return false, err
		}
		if err := r.reconcileIgrisRabbitMQStack(ctx, st, shadowNS); err != nil {
			return false, err
		}
		igrisRMQReady, err := r.igrisRabbitMQDeploymentReady(ctx, st, shadowNS)
		if err != nil {
			return false, err
		}
		if !igrisRMQReady {
			_ = r.patchStatusIgrisRabbitMQ(ctx, st, "Progressing", "waiting for igris-rabbitmq", shadowNS, "Progressing")
			return false, nil
		}
		if err := r.reconcileEgressRelayRabbitMQStack(ctx, st, shadowNS); err != nil {
			return false, err
		}
		egressRelayReady, err := r.egressRelayRabbitMQDeploymentReady(ctx, st, shadowNS)
		if err != nil {
			return false, err
		}
		if !egressRelayReady {
			_ = r.patchStatusIgrisRabbitMQ(ctx, st, "Progressing", "waiting for egress-relay-rabbitmq", shadowNS, "Progressing")
			return false, nil
		}
	}

	if needsHTTPTCPIngress(st) {
		if err := r.reconcileIgrisConfigMap(ctx, st, shadowNS); err != nil {
			return false, err
		}
		if err := r.reconcileIgrisDeployment(ctx, st, shadowNS); err != nil {
			return false, err
		}
		if err := r.reconcileIgrisService(ctx, st, shadowNS); err != nil {
			return false, err
		}
		igrisReady, err := r.igrisDeploymentReady(ctx, st, shadowNS)
		if err != nil {
			return false, err
		}
		if !igrisReady {
			_ = r.patchStatus(ctx, st, "Progressing", "waiting for Igris", shadowNS)
			return false, nil
		}
	}

	if needsEgressRelayRabbitMQ(st) && needsHTTPTCPIngress(st) {
		if err := r.reconcileEgressRelayRabbitMQStack(ctx, st, shadowNS); err != nil {
			return false, err
		}
		egressRelayReady, err := r.egressRelayRabbitMQDeploymentReady(ctx, st, shadowNS)
		if err != nil {
			return false, err
		}
		if !egressRelayReady {
			_ = r.patchStatus(ctx, st, "Progressing", "waiting for egress-relay-rabbitmq", shadowNS)
			return false, nil
		}
	}

	return true, nil
}

func (r *ShadowTestReconciler) reconcileShadowWorkloads(
	ctx context.Context,
	st *enginev1alpha1.ShadowTest,
	shadowNS string,
	env []corev1.EnvVar,
	target *appsv1.Deployment,
) (bool, error) {
	for _, step := range []struct {
		role  string
		image string
	}{
		{roleControlA, st.Spec.OldImage},
		{roleControlB, st.Spec.OldImage},
		{roleCandidate, st.Spec.NewImage},
	} {
		if err := r.reconcileEnvoyConfigMap(ctx, st, shadowNS, step.role); err != nil {
			return false, err
		}
		if err := r.reconcileShadowDeployment(ctx, st, shadowNS, step.role, step.image, env); err != nil {
			return false, err
		}
		if err := r.reconcileShadowService(ctx, st, shadowNS, step.role); err != nil {
			return false, err
		}
	}

	shadowsReady, err := r.shadowDeploymentsReady(ctx, st, shadowNS)
	if err != nil {
		return false, err
	}
	return shadowsReady, nil
}

func (r *ShadowTestReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&enginev1alpha1.ShadowTest{}).
		Watches(&appsv1.Deployment{}, handler.EnqueueRequestsFromMapFunc(r.mapDeploymentToShadowTests)).
		Named("shadowtest").
		Complete(r)
}
