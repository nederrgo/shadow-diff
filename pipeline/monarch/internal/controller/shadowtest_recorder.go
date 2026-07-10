package controller

import (
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	enginev1alpha1 "github.com/shadow-diff/monarch/api/v1alpha1"
)

func recorderDeploymentName(st *enginev1alpha1.ShadowTest) string {
	return sanitizeForDNS(fmt.Sprintf("%s-recorder", st.Name))
}

func recorderServiceName(st *enginev1alpha1.ShadowTest) string {
	return recorderDeploymentName(st)
}

func recorderHostFor(st *enginev1alpha1.ShadowTest, shadowNS string) string {
	host := shadowServiceHost(shadowNS, recorderServiceName(st))
	return fmt.Sprintf("%s:%d", host, recorderServicePort)
}

func (r *ShadowTestReconciler) reconcileRecorderDeployment(
	ctx context.Context,
	st *enginev1alpha1.ShadowTest,
	shadowNS string,
) error {
	name := recorderDeploymentName(st)
	labels := map[string]string{
		labelManagedBy:           valueManagedBy,
		labelShadowTestName:      st.Name,
		labelShadowTestCRNS:      st.Namespace,
		labelShadowTestUID:       string(st.UID),
		"app.kubernetes.io/name": "recorder",
	}

	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: shadowNS,
			Name:      name,
		},
	}
	replicas := int32(1)
	_, err := ctrl.CreateOrPatch(ctx, r.Client, deploy, func() error {
		deploy.Labels = labels
		deploy.Spec.Replicas = &replicas
		deploy.Spec.Selector = &metav1.LabelSelector{MatchLabels: labels}
		deploy.Spec.Template.ObjectMeta.Labels = labels

		container := corev1.Container{
			Name:            containerRecorder,
			Image:           recorderImageFor(st),
			ImagePullPolicy: corev1.PullIfNotPresent,
			Ports: []corev1.ContainerPort{
				{
					Name:          "http",
					ContainerPort: recorderServicePort,
					Protocol:      corev1.ProtocolTCP,
				},
				{
					Name:          "otlp-grpc",
					ContainerPort: recorderOTLPPort,
					Protocol:      corev1.ProtocolTCP,
				},
			},
			Env: []corev1.EnvVar{
				{Name: envShopHTTPURL, Value: fmt.Sprintf("http://%s", shopHTTPHostFor(shadowNS))},
				{Name: envRecorderListenAddr, Value: ":8080"},
				{Name: envRecorderOTLPGRPCAddr, Value: ":4317"},
			},
			Resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("50m"),
					corev1.ResourceMemory: resource.MustParse("64Mi"),
				},
				Limits: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("500m"),
					corev1.ResourceMemory: resource.MustParse("256Mi"),
				},
			},
		}
		deploy.Spec.Template.Spec.Containers = []corev1.Container{container}
		return nil
	})
	return err
}

func (r *ShadowTestReconciler) reconcileRecorderService(
	ctx context.Context,
	st *enginev1alpha1.ShadowTest,
	shadowNS string,
) error {
	name := recorderServiceName(st)
	labels := map[string]string{
		labelManagedBy:           valueManagedBy,
		labelShadowTestName:      st.Name,
		"app.kubernetes.io/name": "recorder",
	}
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: shadowNS,
			Name:      name,
		},
	}
	_, err := ctrl.CreateOrPatch(ctx, r.Client, svc, func() error {
		svc.Labels = labels
		svc.Spec.Selector = labels
		svc.Spec.Ports = []corev1.ServicePort{
			{
				Name:       "http",
				Port:       recorderServicePort,
				TargetPort: intstr.FromInt32(recorderServicePort),
				Protocol:   corev1.ProtocolTCP,
			},
			{
				Name:       "otlp-grpc",
				Port:       recorderOTLPPort,
				TargetPort: intstr.FromInt32(recorderOTLPPort),
				Protocol:   corev1.ProtocolTCP,
			},
		}
		return nil
	})
	return err
}

func (r *ShadowTestReconciler) recorderDeploymentReady(
	ctx context.Context,
	st *enginev1alpha1.ShadowTest,
	shadowNS string,
) (bool, error) {
	var deploy appsv1.Deployment
	key := client.ObjectKey{Namespace: shadowNS, Name: recorderDeploymentName(st)}
	if err := r.Get(ctx, key, &deploy); err != nil {
		return false, err
	}
	return deploy.Status.AvailableReplicas > 0, nil
}

func (r *ShadowTestReconciler) reconcileRecorderStack(
	ctx context.Context,
	st *enginev1alpha1.ShadowTest,
	shadowNS string,
) error {
	if err := r.reconcileRecorderDeployment(ctx, st, shadowNS); err != nil {
		return err
	}
	return r.reconcileRecorderService(ctx, st, shadowNS)
}
