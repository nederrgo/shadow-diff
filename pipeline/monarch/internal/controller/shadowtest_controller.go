// +kubebuilder:rbac:groups="",resources=namespaces,verbs=create;delete;get;list;watch
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch
// +kubebuilder:rbac:groups=apps,resources=replicasets,verbs=get;list;watch
// +kubebuilder:rbac:groups=engine.shadow-diff.io,resources=shadowtests,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=engine.shadow-diff.io,resources=shadowtests/finalizers,verbs=update
// +kubebuilder:rbac:groups=engine.shadow-diff.io,resources=shadowtests/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=engine.shadow-diff.io,resources=kaiselrules,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=engine.shadow-diff.io,resources=kaiselrules/status,verbs=get;update;patch
// Secret reads are not cluster-wide: BERU_DB_SECRET uses a namespaced Role
// (resourceNames) and credentialsSecretRef uses ClusterRole secret-source-reader
// bound only in install-time source namespaces. Writes stay on shadow-workload-role.
// +kubebuilder:rbac:groups="",resources=configmaps;services,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=rolebindings,verbs=create;delete;get;list;patch;update;watch
// Bind on shadow-workload-role is added by config/default/manager_bind_patch.yaml
// (resourceNames must match the kustomize namePrefix). Kubernetes privilege-escalation
// prevention otherwise forbids creating a RoleBinding for permissions the SA does not hold.

package controller

import (
	"context"
	"errors"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
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

// StatusPublisher receives ShadowTest status changes for live streaming.
// Declared here rather than in pkg/grpc so the controller does not depend on the
// transport. Nil disables publishing, which is the default in tests.
type StatusPublisher interface {
	Publish(st *enginev1alpha1.ShadowTest)
}

type ShadowTestReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder

	// StatusPublisher broadcasts status changes to open gRPC streams. Nil is a no-op.
	StatusPublisher StatusPublisher

	// ReplayStarter POSTs Igris admin /v1/replay/start. Nil uses net/http.
	ReplayStarter func(ctx context.Context, url string) (statusCode int, err error)
	// S3Cleaner deletes the ShadowTest S3 prefix on CR deletion. Nil uses s3utils.
	S3Cleaner func(ctx context.Context, st *enginev1alpha1.ShadowTest) error
	// ProdQueueEnsureDeclared / ProdQueueEnsureBound override AMQP broker ops in tests.
	ProdQueueEnsureDeclared func(ctx context.Context, st *enginev1alpha1.ShadowTest) (string, error)
	ProdQueueEnsureBound    func(ctx context.Context, st *enginev1alpha1.ShadowTest) error
}

func (r *ShadowTestReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var shadowTest enginev1alpha1.ShadowTest
	if err := r.Get(ctx, req.NamespacedName, &shadowTest); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	shadowNS := shadowNamespaceForCR(&shadowTest)

	// boot accumulates per-component readiness for status.components as the gates below
	// pass; it is threaded into every progress, failure and Ready status patch.
	boot := enginev1alpha1.ComponentStatus{TargetDeployment: shadowTest.Spec.TargetDeployment}

	if !shadowTest.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, req.NamespacedName, shadowNS)
	}

	if !controllerutil.ContainsFinalizer(&shadowTest, finalizerName) ||
		!controllerutil.ContainsFinalizer(&shadowTest, s3Finalizer) {
		base := shadowTest.DeepCopy()
		controllerutil.AddFinalizer(&shadowTest, finalizerName)
		controllerutil.AddFinalizer(&shadowTest, s3Finalizer)
		if err := r.Patch(ctx, &shadowTest, client.MergeFrom(base)); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	// Sticky Failed: keep autopsy on CR; finish teardown if needed; never recreate stack.
	if shadowTest.Status.Phase == phaseFailed {
		return r.reconcileStickyFailed(ctx, &shadowTest, shadowNS)
	}

	for _, validate := range []func(*enginev1alpha1.ShadowTest) error{
		validateInputs, validateDependencies, validateStorage,
	} {
		if err := validate(&shadowTest); err != nil {
			_ = r.patchBootStatus(ctx, &shadowTest, phaseFailed, err.Error(), shadowNS,
				enginev1alpha1.BootStepValidating, boot)
			return ctrl.Result{}, nil
		}
	}

	var target appsv1.Deployment
	targetKey := types.NamespacedName{Namespace: targetNamespaceFor(&shadowTest), Name: shadowTest.Spec.TargetDeployment}
	if err := r.Get(ctx, targetKey, &target); err != nil {
		if apierrors.IsNotFound(err) {
			msg := fmt.Sprintf("target Deployment %s/%s not found", targetNamespaceFor(&shadowTest), shadowTest.Spec.TargetDeployment)
			log.Info(msg)
			return r.markBootFailed(ctx, &shadowTest, shadowNS, msg, boot)
		}
		return ctrl.Result{}, err
	}

	if err := resolveSpecDefaults(&shadowTest, &target); err != nil {
		msg := fmt.Sprintf("cannot resolve spec defaults from target: %s", err)
		log.Info(msg)
		return r.markBootFailed(ctx, &shadowTest, shadowNS, msg, boot)
	}

	patched, err := r.ensureOldImage(ctx, &shadowTest, &target)
	if err != nil {
		msg := fmt.Sprintf("cannot pin spec.oldImage from target: %s", err)
		log.Info(msg)
		return r.markBootFailed(ctx, &shadowTest, shadowNS, msg, boot)
	}
	if patched {
		return ctrl.Result{Requeue: true}, nil
	}

	if len(shadowTest.Spec.Inputs) == 0 && shadowTest.Spec.TargetDeployment != "" && !httpIngressCaptureEnabled(&shadowTest, &target) {
		log.Info("live capture inactive: no HTTP/TCP ingress input matched target ports",
			"level", "warn",
			"shadowtest", fmt.Sprintf("%s/%s", shadowTest.Namespace, shadowTest.Name))
	}

	if len(shadowNS) > 63 {
		msg := "Shadow namespace name exceeds 63 characters. Please use a shorter ShadowTest name."
		_ = r.patchBootStatus(ctx, &shadowTest, phaseFailed, msg, shadowNS,
			enginev1alpha1.BootStepValidating, boot)
		return ctrl.Result{}, nil
	}

	if err := r.ensureShadowNamespace(ctx, &shadowTest, shadowNS); err != nil {
		if errors.Is(err, ErrNamespaceCollision) {
			_ = r.patchBootStatus(ctx, &shadowTest, phaseFailed, err.Error(), shadowNS,
				enginev1alpha1.BootStepValidating, boot)
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	if _, err := r.ensureSessionID(ctx, &shadowTest); err != nil {
		_ = r.patchBootStatus(ctx, &shadowTest, phaseFailed, err.Error(), shadowNS,
			enginev1alpha1.BootStepValidating, boot)
		return ctrl.Result{}, nil
	}
	if err := r.syncStorageSecret(ctx, &shadowTest, shadowNS); err != nil {
		_ = r.patchBootStatus(ctx, &shadowTest, phaseFailed, err.Error(), shadowNS,
			enginev1alpha1.BootStepValidating, boot)
		return ctrl.Result{}, err
	}
	if err := r.syncBeruDBSecret(ctx, &shadowTest, shadowNS); err != nil {
		_ = r.patchBootStatus(ctx, &shadowTest, phaseFailed, err.Error(), shadowNS,
			enginev1alpha1.BootStepValidating, boot)
		return ctrl.Result{}, err
	}

	mode := operatingMode(&shadowTest)
	var captureTargets []string
	kaiselPhase := ""

	// A ShadowTest with no AMQP ingress is trivially "bound"; record mode flips this
	// after QueueBind. Replay never taps the prod broker, so it stays false there.
	boot.AMQPBound = !needsAMQPIngress(&shadowTest)
	boot.IngressDrivers = ingressDriversFromSpec(&shadowTest)

	if mode == modeRecord {
		if err := r.clearReplayState(ctx, &shadowTest); err != nil {
			return ctrl.Result{}, err
		}
		if err := r.deleteShadowRoleWorkloads(ctx, &shadowTest, shadowNS); err != nil {
			return ctrl.Result{}, err
		}
	} else {
		// Replay: ABC + Shop/Igris; no KaiselRule.
		if err := r.deleteKaiselRule(ctx, &shadowTest); err != nil {
			return ctrl.Result{}, err
		}
		kaiselPhase = capturePhaseDisabled
		// Mint execution id before beru-local so REPLAY_EXECUTION_ID is on the pod.
		if _, err := r.ensureReplayExecutionID(ctx, &shadowTest); err != nil {
			return ctrl.Result{}, err
		}
	}

	if done, result, err := r.runBootGates(ctx, &shadowTest, shadowNS, &boot, []bootGate{
		r.localBeruBootGate(&shadowTest, shadowNS),
	}); !done {
		return result, err
	}

	env, warnMsg := envFromTarget(&target)

	if mode == modeReplay {
		// Shop stays ahead of ABC so egress mocks are ready before shadow pods start.
		if done, result, err := r.runBootGates(ctx, &shadowTest, shadowNS, &boot,
			r.replayBootGates(&shadowTest, shadowNS, env, &target)); !done {
			return result, err
		}

		if requeue, err := r.maybeTriggerReplay(ctx, &shadowTest, shadowNS); err != nil {
			_ = r.patchBootStatus(ctx, &shadowTest, phaseProgressing,
				fmt.Sprintf("waiting to trigger replay: %v", err), shadowNS,
				enginev1alpha1.BootStepProvisioningShadow, boot)
			return ctrl.Result{RequeueAfter: bootRequeueInterval}, nil
		} else if requeue {
			_ = r.patchBootStatus(ctx, &shadowTest, phaseProgressing, "waiting to trigger replay", shadowNS,
				enginev1alpha1.BootStepProvisioningShadow, boot)
			return ctrl.Result{RequeueAfter: bootRequeueInterval}, nil
		}
	} else {
		// Record: unbound queue + Shop/Igris → KaiselRule → AMQP bind.
		rec := r.reconcileRecordMode(ctx, &shadowTest, shadowNS, &target, boot)
		boot = rec.components
		if rec.err != nil {
			return rec.requeue, rec.err
		}
		if !rec.done {
			return rec.requeue, nil
		}
		captureTargets = rec.captureTargets
		kaiselPhase = rec.kaiselPhase
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
		msg = fmt.Sprintf("%s mode ready with ingress [%s]", mode, listenersSummary(&shadowTest))
	} else {
		msg = fmt.Sprintf("%s; %s mode; ingress [%s]", msg, mode, listenersSummary(&shadowTest))
	}
	if kaiselPhase != "" && kaiselPhase != capturePhaseDisabled {
		msg = fmt.Sprintf("%s; Kaisel %s", msg, kaiselPhase)
	}

	if err := r.patchStatusReady(ctx, &shadowTest, msg, shadowNS,
		captureTargets, kaiselPhase, igrisEndpoint, igrisRMQPhase, boot); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}
func (r *ShadowTestReconciler) reconcileShadowWorkloads(
	ctx context.Context,
	st *enginev1alpha1.ShadowTest,
	shadowNS string,
	env []corev1.EnvVar,
	target *appsv1.Deployment,
) (map[string]bool, workloadWaitReason, error) {
	if err := r.reconcileShadowWorkloadResources(ctx, st, shadowNS, env, target); err != nil {
		return nil, workloadWaitReason{}, err
	}
	return r.shadowDeploymentsReady(ctx, st, shadowNS)
}

func (r *ShadowTestReconciler) reconcileShadowWorkloadResources(
	ctx context.Context,
	st *enginev1alpha1.ShadowTest,
	shadowNS string,
	env []corev1.EnvVar,
	target *appsv1.Deployment,
) error {
	if st.Spec.OldImage == "" {
		return fmt.Errorf("spec.oldImage is empty; control-a/b require a baseline image")
	}
	for _, step := range []struct {
		role  string
		image string
	}{
		{roleControlA, st.Spec.OldImage},
		{roleControlB, st.Spec.OldImage},
		{roleCandidate, st.Spec.NewImage},
	} {
		if err := r.reconcileEnvoyConfigMap(ctx, st, shadowNS, step.role); err != nil {
			return err
		}
		if err := r.reconcileShadowDeployment(ctx, st, shadowNS, step.role, step.image, env); err != nil {
			return err
		}
		if err := r.reconcileShadowService(ctx, st, shadowNS, step.role); err != nil {
			return err
		}
	}
	return nil
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
		var dep appsv1.Deployment
		if err := r.Get(ctx, types.NamespacedName{
			Namespace: targetNamespaceFor(&st),
			Name:      st.Spec.TargetDeployment,
		}, &dep); err != nil {
			continue
		}
		owned, err := r.podOwnedByDeployment(ctx, pod, &dep)
		if err != nil || !owned {
			continue
		}
		out = append(out, reconcile.Request{
			NamespacedName: types.NamespacedName{Namespace: st.Namespace, Name: st.Name},
		})
	}
	return out
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
