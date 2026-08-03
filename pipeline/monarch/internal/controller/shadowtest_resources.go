package controller

import (
	"context"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	enginev1alpha1 "github.com/shadow-diff/monarch/api/v1alpha1"
)

func (r *ShadowTestReconciler) reconcileDelete(ctx context.Context, nn types.NamespacedName, shadowNS string) (ctrl.Result, error) {
	var shadowTest enginev1alpha1.ShadowTest
	if err := r.Get(ctx, nn, &shadowTest); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !controllerutil.ContainsFinalizer(&shadowTest, finalizerName) &&
		!controllerutil.ContainsFinalizer(&shadowTest, s3Finalizer) {
		return ctrl.Result{}, nil
	}

	// Event 1: status=Deleting so Tusk/UI show teardown in progress. DeepEqual in
	// patchStatusCore makes this a single publish across requeues.
	if err := r.patchStatusCore(ctx, &shadowTest,
		statusBase(shadowTest.Generation, phaseDeleting, "tearing down shadow stack", shadowNS),
	); err != nil {
		return ctrl.Result{}, err
	}

	if hasRabbitMQInput(&shadowTest) {
		if err := r.deleteProdShadowQueue(ctx, &shadowTest); err != nil {
			return ctrl.Result{RequeueAfter: 10 * time.Second}, err
		}
	}

	if err := r.deleteKaiselRule(ctx, &shadowTest); err != nil {
		return ctrl.Result{RequeueAfter: 5 * time.Second}, err
	}

	var ns corev1.Namespace
	err := r.Get(ctx, types.NamespacedName{Name: shadowNS}, &ns)
	if apierrors.IsNotFound(err) {
		if err := r.cleanupS3IfNeeded(ctx, &shadowTest); err != nil {
			return ctrl.Result{RequeueAfter: 5 * time.Second}, err
		}
		if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			var fresh enginev1alpha1.ShadowTest
			if err := r.Get(ctx, nn, &fresh); err != nil {
				return client.IgnoreNotFound(err)
			}
			if !controllerutil.ContainsFinalizer(&fresh, finalizerName) &&
				!controllerutil.ContainsFinalizer(&fresh, s3Finalizer) {
				return nil
			}
			base := fresh.DeepCopy()
			controllerutil.RemoveFinalizer(&fresh, s3Finalizer)
			controllerutil.RemoveFinalizer(&fresh, finalizerName)
			return r.Patch(ctx, &fresh, client.MergeFrom(base))
		}); err != nil {
			return ctrl.Result{}, err
		}
		// Event 2: stream-only tombstone after finalizers drop. The CR is gone
		// (or about to be); Tusk must drop its cache entry.
		r.publishDeleted(&shadowTest)
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

// publishDeleted emits a PHASE_DELETED tombstone. It does not write the CR —
// by this point finalizers are gone and the API server may already have deleted it.
func (r *ShadowTestReconciler) publishDeleted(st *enginev1alpha1.ShadowTest) {
	if r.StatusPublisher == nil || st == nil {
		return
	}
	tomb := st.DeepCopy()
	tomb.Status.Phase = phaseDeleted
	tomb.Status.Message = "deleted"
	r.StatusPublisher.Publish(tomb)
}

func (r *ShadowTestReconciler) ensureShadowNamespace(ctx context.Context, st *enginev1alpha1.ShadowTest, name string) error {
	var ns corev1.Namespace
	err := r.Get(ctx, types.NamespacedName{Name: name}, &ns)
	if apierrors.IsNotFound(err) {
		ns = corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: name,
				Labels: map[string]string{
					labelManagedBy:      valueManagedBy,
					labelShadowTestName: st.Name,
					labelShadowTestCRNS: st.Namespace,
					labelShadowTestUID:  string(st.UID),
				},
			},
		}
		return r.Create(ctx, &ns)
	}
	return err
}

func (r *ShadowTestReconciler) reconcileShadowDeployment(
	ctx context.Context,
	st *enginev1alpha1.ShadowTest,
	shadowNS, role, image string,
	env []corev1.EnvVar,
) error {
	deployName := sanitizeForDNS(fmt.Sprintf("%s-%s", st.Name, role))
	podLabels := deploymentPodLabels(st, role)

	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: shadowNS,
			Name:      deployName,
		},
	}

	_, err := ctrl.CreateOrPatch(ctx, r.Client, deploy, func() error {
		if deploy.Labels == nil {
			deploy.Labels = map[string]string{}
		}
		for k, v := range podLabels {
			deploy.Labels[k] = v
		}

		replicas := shadowRoleReplicas
		deploy.Spec.Replicas = &replicas
		deploy.Spec.Selector = &metav1.LabelSelector{MatchLabels: podLabels}
		deploy.Spec.Template.ObjectMeta.Labels = podLabels
		cmName := envoyConfigMapName(st, role)
		deploy.Spec.Template.Spec.InitContainers = []corev1.Container{
			{
				Name:  containerIptablesSetup,
				Image: iptablesInitImage,
				SecurityContext: &corev1.SecurityContext{
					Capabilities: &corev1.Capabilities{
						Add: []corev1.Capability{"NET_ADMIN"},
					},
				},
				Command: []string{"/bin/sh", "-c", iptablesSetupScript},
			},
		}
		deploy.Spec.Template.Spec.Volumes = []corev1.Volume{
			{
				Name: volumeNameEnvoyConfig,
				VolumeSource: corev1.VolumeSource{
					ConfigMap: &corev1.ConfigMapVolumeSource{
						LocalObjectReference: corev1.LocalObjectReference{Name: cmName},
					},
				},
			},
		}
		beruAddr := beruGRPCAddressFor(st, shadowNS)
		baseEnv := append([]corev1.EnvVar{}, env...)
		baseEnv = append(baseEnv, dependencyEnvVarsForRole(st, shadowNS, role)...)
		appEnv := baseEnv
		deploy.Spec.Template.Spec.Containers = []corev1.Container{
			{
				Name:  containerApp,
				Image: image,
				Ports: appContainerPortsFor(st),
				Env:   appEnv,
			},
			{
				Name:            containerEnvoySidecar,
				Image:           envoyImageFor(),
				ImagePullPolicy: envoyImagePullPolicy,
				Args:            []string{"-c", "/etc/envoy/envoy.yaml", "--log-level", "info"},
				Ports:           envoyContainerPorts(st),
				Env: []corev1.EnvVar{
					{Name: envShadowRole, Value: role},
					{Name: envShadowTestName, Value: st.Name},
					{Name: envBeruGRPCAddress, Value: beruAddr},
				},
				VolumeMounts: []corev1.VolumeMount{
					{Name: volumeNameEnvoyConfig, MountPath: "/etc/envoy", ReadOnly: true},
				},
			},
		}
		// Database egress capture. Only present when the ShadowTest declares a
		// dependency shadow-soldier can parse, so a ShadowTest with none (or with
		// RabbitMQ only, whose egress the Firehose relay already covers) keeps the
		// two-container pod it has today.
		soldier, err := shadowSoldierContainer(st, shadowNS, role)
		if err != nil {
			return err
		}
		if soldier != nil {
			deploy.Spec.Template.Spec.Containers = append(deploy.Spec.Template.Spec.Containers, *soldier)
		}
		return nil
	})
	return err
}

// statusMutator edits a status in place; composed by the patchStatus* wrappers below.
type statusMutator func(*enginev1alpha1.ShadowTestStatus)

// patchStatusCore is the single writer for ShadowTestStatus. The base snapshot is taken
// here, before the mutators run — mutating st.Status beforehand yields an empty merge
// patch and silently drops the change.
func (r *ShadowTestReconciler) patchStatusCore(
	ctx context.Context,
	st *enginev1alpha1.ShadowTest,
	apply ...statusMutator,
) error {
	base := st.DeepCopy()
	for _, fn := range apply {
		fn(&st.Status)
	}
	if equality.Semantic.DeepEqual(base.Status, st.Status) {
		return nil // converged; skip the API round-trip
	}
	if err := r.Status().Patch(ctx, st, client.MergeFrom(base)); err != nil {
		return err
	}
	// Publish only after a successful write, and only here: this is the single
	// status writer, so live streams see exactly one update per real transition
	// and nothing at all once the ShadowTest has converged.
	if r.StatusPublisher != nil {
		r.StatusPublisher.Publish(st)
	}
	return nil
}

// statusBase sets phase/message/namespace and mirrors the phase onto the standard
// Ready/Progressing/Degraded conditions. Every status write goes through it.
func statusBase(generation int64, phase, message, shadowNS string) statusMutator {
	reason := phase
	if reason == "" {
		reason = "Unknown"
	}
	return func(s *enginev1alpha1.ShadowTestStatus) {
		s.Phase = phase
		s.Message = message
		s.ShadowNamespace = shadowNS

		set := func(condType string, active bool) {
			status := metav1.ConditionFalse
			if active {
				status = metav1.ConditionTrue
			}
			// SetStatusCondition only moves LastTransitionTime when Status flips, so a
			// converged reconcile stays DeepEqual to the previous status.
			meta.SetStatusCondition(&s.Conditions, metav1.Condition{
				Type:               condType,
				Status:             status,
				Reason:             reason,
				Message:            message,
				ObservedGeneration: generation,
			})
		}
		set(enginev1alpha1.ConditionReady, phase == phaseReady)
		set(enginev1alpha1.ConditionProgressing, phase == phaseProgressing || phase == phaseDeleting)
		set(enginev1alpha1.ConditionDegraded, phase == phaseFailed)
	}
}

// statusBoot records the topology-graph fields consumed by Tusk.
func statusBoot(step enginev1alpha1.BootStep, comp enginev1alpha1.ComponentStatus) statusMutator {
	return func(s *enginev1alpha1.ShadowTestStatus) {
		s.BootStep = step
		s.Components = comp
	}
}

// statusExtras sets the endpoint/phase detail fields; empty values leave them untouched.
func statusExtras(
	captureTargets []string,
	kaiselPhase, igrisEndpoint, igrisRabbitMQPhase string,
) statusMutator {
	return func(s *enginev1alpha1.ShadowTestStatus) {
		if captureTargets != nil {
			s.CaptureTargets = captureTargets
		}
		if kaiselPhase != "" {
			s.KaiselPhase = kaiselPhase
		}
		if igrisEndpoint != "" {
			s.IgrisEndpoint = igrisEndpoint
		}
		if igrisRabbitMQPhase != "" {
			s.IgrisRabbitMQPhase = igrisRabbitMQPhase
		}
	}
}

func (r *ShadowTestReconciler) patchStatus(ctx context.Context, st *enginev1alpha1.ShadowTest, phase, message, shadowNS string) error {
	return r.patchStatusCore(ctx, st, statusBase(st.Generation, phase, message, shadowNS))
}

// patchBootStatus is the progress-reporting variant used by every boot gate.
func (r *ShadowTestReconciler) patchBootStatus(
	ctx context.Context,
	st *enginev1alpha1.ShadowTest,
	phase, message, shadowNS string,
	step enginev1alpha1.BootStep,
	comp enginev1alpha1.ComponentStatus,
) error {
	return r.patchStatusCore(ctx, st,
		statusBase(st.Generation, phase, message, shadowNS),
		statusBoot(step, comp),
	)
}

func (r *ShadowTestReconciler) patchStatusIgrisRabbitMQ(
	ctx context.Context,
	st *enginev1alpha1.ShadowTest,
	phase, message, shadowNS, igrisRMQPhase string,
) error {
	return r.patchStatusFull(ctx, st, phase, message, shadowNS, nil, "", "", igrisRMQPhase)
}

func (r *ShadowTestReconciler) patchStatusFull(
	ctx context.Context,
	st *enginev1alpha1.ShadowTest,
	phase, message, shadowNS string,
	captureTargets []string,
	kaiselPhase, igrisEndpoint, igrisRabbitMQPhase string,
) error {
	return r.patchStatusCore(ctx, st,
		statusBase(st.Generation, phase, message, shadowNS),
		statusExtras(captureTargets, kaiselPhase, igrisEndpoint, igrisRabbitMQPhase),
	)
}

// patchStatusReady is the terminal converged write: detail fields plus boot state.
func (r *ShadowTestReconciler) patchStatusReady(
	ctx context.Context,
	st *enginev1alpha1.ShadowTest,
	message, shadowNS string,
	captureTargets []string,
	kaiselPhase, igrisEndpoint, igrisRabbitMQPhase string,
	comp enginev1alpha1.ComponentStatus,
) error {
	return r.patchStatusCore(ctx, st,
		statusBase(st.Generation, phaseReady, message, shadowNS),
		statusExtras(captureTargets, kaiselPhase, igrisEndpoint, igrisRabbitMQPhase),
		statusBoot(enginev1alpha1.BootStepReady, comp),
	)
}
