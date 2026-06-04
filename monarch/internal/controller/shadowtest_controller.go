// +kubebuilder:rbac:groups="",resources=namespaces,verbs=create;delete;get;list;watch
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=engine.shadow-diff.io,resources=shadowtests,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=engine.shadow-diff.io,resources=shadowtests/finalizers,verbs=update
// +kubebuilder:rbac:groups=engine.shadow-diff.io,resources=shadowtests/status,verbs=get;update;patch
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch
// +kubebuilder:rbac:groups=apps,resources=daemonsets,verbs=get;list;watch;create;update;patch;delete

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
	targetKey := types.NamespacedName{Namespace: shadowTest.Spec.TargetNamespace, Name: shadowTest.Spec.TargetDeployment}
	if err := r.Get(ctx, targetKey, &target); err != nil {
		if apierrors.IsNotFound(err) {
			msg := fmt.Sprintf("target Deployment %s/%s not found", shadowTest.Spec.TargetNamespace, shadowTest.Spec.TargetDeployment)
			log.Info(msg)
			_ = r.patchStatus(ctx, &shadowTest, "Failed", msg, shadowNS)
			return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
		}
		return ctrl.Result{}, err
	}

	if err := r.ensureShadowNamespace(ctx, &shadowTest, shadowNS); err != nil {
		return ctrl.Result{}, err
	}

	if err := r.reconcileShadowDependencies(ctx, &shadowTest, shadowNS); err != nil {
		_ = r.patchStatus(ctx, &shadowTest, "Failed", err.Error(), shadowNS)
		return ctrl.Result{}, err
	}

	env, warnMsg := envFromTarget(&target)

	amqpOnly := isAMQPOnlyShadowTest(&shadowTest)

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

	if amqpOnly {
		if _, err := r.ensureProdShadowQueue(ctx, &shadowTest); err != nil {
			_ = r.patchStatus(ctx, &shadowTest, "Failed", err.Error(), shadowNS)
			return ctrl.Result{}, err
		}
		if err := r.refreshShadowTest(ctx, req.NamespacedName, &shadowTest); err != nil {
			return ctrl.Result{}, err
		}
		if err := r.reconcileIgrisRabbitMQStack(ctx, &shadowTest, shadowNS); err != nil {
			_ = r.patchStatus(ctx, &shadowTest, "Failed", err.Error(), shadowNS)
			return ctrl.Result{}, err
		}
		igrisRMQReady, err := r.igrisRabbitMQDeploymentReady(ctx, &shadowTest, shadowNS)
		if err != nil {
			return ctrl.Result{}, err
		}
		if !igrisRMQReady {
			_ = r.patchStatusIgrisRabbitMQ(ctx, &shadowTest, "Progressing", "waiting for igris-rabbitmq", shadowNS, "Progressing")
			return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
		}
	} else {
		if err := r.reconcileIgrisConfigMap(ctx, &shadowTest, shadowNS); err != nil {
			_ = r.patchStatus(ctx, &shadowTest, "Failed", err.Error(), shadowNS)
			return ctrl.Result{}, err
		}
		if err := r.reconcileIgrisDeployment(ctx, &shadowTest, shadowNS); err != nil {
			_ = r.patchStatus(ctx, &shadowTest, "Failed", err.Error(), shadowNS)
			return ctrl.Result{}, err
		}
		if err := r.reconcileIgrisService(ctx, &shadowTest, shadowNS); err != nil {
			_ = r.patchStatus(ctx, &shadowTest, "Failed", err.Error(), shadowNS)
			return ctrl.Result{}, err
		}
	}

	for _, step := range []struct {
		role  string
		image string
	}{
		{roleControlA, shadowTest.Spec.OldImage},
		{roleControlB, shadowTest.Spec.OldImage},
		{roleCandidate, shadowTest.Spec.NewImage},
	} {
		if err := r.reconcileEnvoyConfigMap(ctx, &shadowTest, shadowNS, step.role); err != nil {
			_ = r.patchStatus(ctx, &shadowTest, "Failed", err.Error(), shadowNS)
			return ctrl.Result{}, err
		}
		if err := r.reconcileShadowDeployment(ctx, &shadowTest, shadowNS, step.role, step.image, env); err != nil {
			_ = r.patchStatus(ctx, &shadowTest, "Failed", err.Error(), shadowNS)
			return ctrl.Result{}, err
		}
		if err := r.reconcileShadowService(ctx, &shadowTest, shadowNS, step.role); err != nil {
			_ = r.patchStatus(ctx, &shadowTest, "Failed", err.Error(), shadowNS)
			return ctrl.Result{}, err
		}
	}

	shadowsReady, err := r.shadowDeploymentsReady(ctx, &shadowTest, shadowNS)
	if err != nil {
		return ctrl.Result{}, err
	}
	if !shadowsReady {
		_ = r.patchStatus(ctx, &shadowTest, "Progressing", "waiting for shadow Deployments", shadowNS)
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}

	if !amqpOnly {
		igrisReady, err := r.igrisDeploymentReady(ctx, &shadowTest, shadowNS)
		if err != nil {
			return ctrl.Result{}, err
		}
		if !igrisReady {
			_ = r.patchStatus(ctx, &shadowTest, "Progressing", "waiting for Igris", shadowNS)
			return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
		}
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

	captureIPs, siphonPhase, err := r.reconcileSiphonCapture(ctx, &shadowTest, shadowNS, &target)
	if err != nil {
		log.Error(err, "Siphon capture reconcile failed")
		siphonPhase = "Degraded"
	}

	var igrisEndpoint string
	igrisRMQPhase := ""
	if amqpOnly {
		igrisRMQPhase = "Ready"
		igrisEndpoint = fmt.Sprintf("amqp queue %s; igris-rabbitmq %s",
			shadowTest.Status.AmqpQueueName,
			shadowServiceHost(shadowNS, igrisRabbitMQServiceName(&shadowTest)))
	} else {
		igrisHost := shadowServiceHost(shadowNS, igrisServiceName(&shadowTest))
		igrisEndpoint = fmt.Sprintf("%s:%d", igrisHost, shadowTest.Spec.ServicePort)
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

	if err := r.patchStatusFull(ctx, &shadowTest, "Ready", msg, shadowNS, captureIPs, siphonPhase, igrisEndpoint, igrisRMQPhase); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

func (r *ShadowTestReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&enginev1alpha1.ShadowTest{}).
		Watches(&corev1.Pod{}, handler.EnqueueRequestsFromMapFunc(r.mapPodToShadowTests)).
		Named("shadowtest").
		Complete(r)
}
