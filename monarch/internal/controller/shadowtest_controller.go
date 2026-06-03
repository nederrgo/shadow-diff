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
		controllerutil.AddFinalizer(&shadowTest, finalizerName)
		if err := r.Update(ctx, &shadowTest); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	if err := validateInputs(&shadowTest); err != nil {
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

	env, warnMsg := envFromTarget(&target)

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

	shadowsReady, err := r.shadowDeploymentsReady(ctx, &shadowTest, shadowNS)
	if err != nil {
		return ctrl.Result{}, err
	}
	if !shadowsReady {
		_ = r.patchStatus(ctx, &shadowTest, "Progressing", "waiting for shadow Deployments", shadowNS)
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}

	igrisReady, err := r.igrisDeploymentReady(ctx, &shadowTest, shadowNS)
	if err != nil {
		return ctrl.Result{}, err
	}
	if !igrisReady {
		_ = r.patchStatus(ctx, &shadowTest, "Progressing", "waiting for Igris", shadowNS)
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}

	captureIPs, siphonPhase, err := r.reconcileSiphonCapture(ctx, &shadowTest, shadowNS, &target)
	if err != nil {
		log.Error(err, "Siphon capture reconcile failed")
		siphonPhase = "Degraded"
	}

	igrisHost := shadowServiceHost(shadowNS, igrisServiceName(&shadowTest))
	igrisEndpoint := fmt.Sprintf("%s:%d", igrisHost, shadowTest.Spec.ServicePort)

	msg := warnMsg
	if msg == "" {
		msg = fmt.Sprintf("shadow environment ready with Igris listeners [%s]", listenersSummary(&shadowTest))
	} else {
		msg = fmt.Sprintf("%s; Igris listeners [%s]", msg, listenersSummary(&shadowTest))
	}
	if siphonPhase != "" && siphonPhase != "Disabled" {
		msg = fmt.Sprintf("%s; Siphon %s", msg, siphonPhase)
	}

	if err := r.patchStatusFull(ctx, &shadowTest, "Ready", msg, shadowNS, captureIPs, siphonPhase, igrisEndpoint); err != nil {
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
