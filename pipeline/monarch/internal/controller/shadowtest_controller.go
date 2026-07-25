// +kubebuilder:rbac:groups="",resources=namespaces,verbs=create;delete;get;list;watch
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=engine.shadow-diff.io,resources=shadowtests,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=engine.shadow-diff.io,resources=shadowtests/finalizers,verbs=update
// +kubebuilder:rbac:groups=engine.shadow-diff.io,resources=shadowtests/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=engine.shadow-diff.io,resources=pixiestreamrules,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=engine.shadow-diff.io,resources=pixiestreamrules/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=engine.shadow-diff.io,resources=pixiestreamrules/finalizers,verbs=update
// +kubebuilder:rbac:groups=engine.shadow-diff.io,resources=kaiselrules,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=engine.shadow-diff.io,resources=kaiselrules/status,verbs=get;update;patch
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
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

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

	if err := resolveSpecDefaults(&shadowTest, &target); err != nil {
		msg := fmt.Sprintf("cannot resolve spec defaults from target: %s", err)
		log.Info(msg)
		_ = r.patchStatus(ctx, &shadowTest, "Failed", msg, shadowNS)
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	if len(shadowTest.Spec.Inputs) == 0 && shadowTest.Spec.TargetDeployment != "" && !httpIngressCaptureEnabled(&shadowTest, &target) {
		log.Info("live capture inactive: no HTTP/TCP ingress input matched target ports",
			"level", "warn",
			"shadowtest", fmt.Sprintf("%s/%s", shadowTest.Namespace, shadowTest.Name))
	}

	if err := r.ensureShadowNamespace(ctx, &shadowTest, shadowNS); err != nil {
		return ctrl.Result{}, err
	}

	// KaiselRule only needs target pod IPs — create it immediately, independent
	// of beru-local/igris/shadow-stack readiness so eBPF capture starts as soon
	// as the target pods exist.
	captureTargets, kaiselPhase, err := r.reconcileKaiselCapture(ctx, &shadowTest, shadowNS, &target)
	if err != nil {
		log.Error(err, "Kaisel capture reconcile failed")
		kaiselPhase = "Degraded"
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

	if err := r.reconcileShop(ctx, &shadowTest, shadowNS); err != nil {
		_ = r.patchStatus(ctx, &shadowTest, "Failed", err.Error(), shadowNS)
		return ctrl.Result{}, err
	}
	shopReady, err := r.shopDeploymentReady(ctx, shadowNS)
	if err != nil {
		return ctrl.Result{}, err
	}
	if !shopReady {
		_ = r.patchStatus(ctx, &shadowTest, "Progressing", "waiting for Shop", shadowNS)
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}
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
	if kaiselPhase != "" && kaiselPhase != "Disabled" {
		msg = fmt.Sprintf("%s; Kaisel %s", msg, kaiselPhase)
	}

	if err := r.patchStatusFull(ctx, &shadowTest, "Ready", msg, shadowNS, captureTargets, kaiselPhase, igrisEndpoint, igrisRMQPhase); err != nil {
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

// podIPChangedPredicate reduces reconcile spam: only enqueue for pod events
// that affect capture targeting (IP assignment, phase transition, deletion).
type podIPChangedPredicate struct{ predicate.Funcs }

func (podIPChangedPredicate) Update(e event.UpdateEvent) bool {
	old, ok := e.ObjectOld.(*corev1.Pod)
	if !ok {
		return true
	}
	neu, ok := e.ObjectNew.(*corev1.Pod)
	if !ok {
		return true
	}
	if old.Status.PodIP != neu.Status.PodIP {
		return true
	}
	if old.Status.Phase != neu.Status.Phase {
		return true
	}
	if (old.DeletionTimestamp == nil) != (neu.DeletionTimestamp == nil) {
		return true
	}
	return false
}

func (r *ShadowTestReconciler) mapPodToShadowTests(ctx context.Context, obj client.Object) []reconcile.Request {
	pod, ok := obj.(*corev1.Pod)
	if !ok {
		return nil
	}
	var list enginev1alpha1.ShadowTestList
	if err := r.List(ctx, &list); err != nil {
		return nil
	}
	var out []reconcile.Request
	for _, st := range list.Items {
		if targetNamespaceFor(&st) != pod.Namespace {
			continue
		}
		// Fetch the target Deployment to get its pod template labels.
		var dep appsv1.Deployment
		if err := r.Get(ctx, types.NamespacedName{
			Namespace: targetNamespaceFor(&st),
			Name:      st.Spec.TargetDeployment,
		}, &dep); err != nil {
			continue
		}
		if labelsMatch(dep.Spec.Template.Labels, pod.Labels) {
			out = append(out, reconcile.Request{
				NamespacedName: types.NamespacedName{Namespace: st.Namespace, Name: st.Name},
			})
		}
	}
	return out
}

// labelsMatch reports whether all key-value pairs in selector are present in labels.
func labelsMatch(selector, labels map[string]string) bool {
	for k, v := range selector {
		if labels[k] != v {
			return false
		}
	}
	return true
}

func (r *ShadowTestReconciler) SetupWithManager(mgr ctrl.Manager, opts controller.Options) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&enginev1alpha1.ShadowTest{}).
		Watches(&appsv1.Deployment{}, handler.EnqueueRequestsFromMapFunc(r.mapDeploymentToShadowTests)).
		Watches(&corev1.Pod{},
			handler.EnqueueRequestsFromMapFunc(r.mapPodToShadowTests),
			builder.WithPredicates(podIPChangedPredicate{})).
		Named("shadowtest").
		WithOptions(opts).
		Complete(r)
}
