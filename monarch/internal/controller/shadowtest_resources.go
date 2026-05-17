package controller

import (
	"context"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
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

	if !controllerutil.ContainsFinalizer(&shadowTest, finalizerName) {
		return ctrl.Result{}, nil
	}

	var ns corev1.Namespace
	err := r.Get(ctx, types.NamespacedName{Name: shadowNS}, &ns)
	if apierrors.IsNotFound(err) {
		if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			var fresh enginev1alpha1.ShadowTest
			if err := r.Get(ctx, nn, &fresh); err != nil {
				return client.IgnoreNotFound(err)
			}
			if !controllerutil.ContainsFinalizer(&fresh, finalizerName) {
				return nil
			}
			base := fresh.DeepCopy()
			controllerutil.RemoveFinalizer(&fresh, finalizerName)
			return r.Patch(ctx, &fresh, client.MergeFrom(base))
		}); err != nil {
			return ctrl.Result{}, err
		}
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

		replicas := int32(1)
		deploy.Spec.Replicas = &replicas
		deploy.Spec.Selector = &metav1.LabelSelector{MatchLabels: podLabels}
		deploy.Spec.Template.ObjectMeta.Labels = podLabels
		cmName := envoyConfigMapName(st, role)
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
		appPort := applicationPortFor(st)
		beruAddr := beruGRPCAddressFor(st)
		deploy.Spec.Template.Spec.Containers = []corev1.Container{
			{
				Name:  "app",
				Image: image,
				Ports: []corev1.ContainerPort{
					{Name: "http", ContainerPort: appPort, Protocol: corev1.ProtocolTCP},
				},
				Env: env,
			},
			{
				Name:            containerEnvoySidecar,
				Image:           envoyImage,
				ImagePullPolicy: envoyImagePullPolicy,
				Args:            []string{"-c", "/etc/envoy/envoy.yaml", "--log-level", "info"},
				Ports: []corev1.ContainerPort{
					{Name: "ingress", ContainerPort: st.Spec.ServicePort, Protocol: corev1.ProtocolTCP},
				},
				Env: []corev1.EnvVar{
					{Name: envShadowRole, Value: role},
					{Name: envBeruGRPCAddress, Value: beruAddr},
				},
				VolumeMounts: []corev1.VolumeMount{
					{Name: volumeNameEnvoyConfig, MountPath: "/etc/envoy", ReadOnly: true},
				},
			},
		}
		return nil
	})
	return err
}

func (r *ShadowTestReconciler) patchStatus(ctx context.Context, st *enginev1alpha1.ShadowTest, phase, message, shadowNS string) error {
	base := st.DeepCopy()
	st.Status.Phase = phase
	st.Status.Message = message
	st.Status.ShadowNamespace = shadowNS
	return r.Status().Patch(ctx, st, client.MergeFrom(base))
}
