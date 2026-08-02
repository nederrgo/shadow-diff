package controller

import (
	"context"
	"fmt"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"

	enginev1alpha1 "github.com/shadow-diff/monarch/api/v1alpha1"
)

func shopServiceName() string { return shopName }

func shopDNSHost(shadowNS string) string {
	return shadowServiceHost(shadowNS, shopName)
}

func shopHTTPHostFor(shadowNS string) string {
	return fmt.Sprintf("%s:%d", shopDNSHost(shadowNS), shopHTTPPort)
}

func shopGRPCAddressFor(shadowNS string) string {
	return fmt.Sprintf("%s:%d", shopDNSHost(shadowNS), shopGRPCPort)
}

func (r *ShadowTestReconciler) reconcileShop(
	ctx context.Context,
	st *enginev1alpha1.ShadowTest,
	shadowNS string,
) error {
	labels := map[string]string{
		labelManagedBy:           valueManagedBy,
		labelShadowTestName:      st.Name,
		labelShadowTestCRNS:      st.Namespace,
		labelShadowTestUID:       string(st.UID),
		"app.kubernetes.io/name": shopName,
	}

	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: shadowNS,
			Name:      shopServiceName(),
		},
	}
	if _, err := ctrl.CreateOrPatch(ctx, r.Client, svc, func() error {
		svc.Labels = labels
		svc.Spec.Selector = labels
		svc.Spec.Ports = []corev1.ServicePort{
			{
				Name:       "grpc",
				Port:       shopGRPCPort,
				TargetPort: intstr.FromInt32(shopGRPCPort),
				Protocol:   corev1.ProtocolTCP,
			},
			{
				Name:       "http",
				Port:       shopHTTPPort,
				TargetPort: intstr.FromInt32(shopHTTPPort),
				Protocol:   corev1.ProtocolTCP,
			},
		}
		return nil
	}); err != nil {
		return err
	}

	name := shopServiceName()
	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: shadowNS,
			Name:      name,
		},
	}
	replicas := int32(1)
	sessionID := strings.TrimSpace(st.Status.CurrentSessionID)
	shopEnv := []corev1.EnvVar{
		{Name: envShopGRPCAddr, Value: fmt.Sprintf(":%d", shopGRPCPort)},
		{Name: envShopHTTPAddr, Value: fmt.Sprintf(":%d", shopHTTPPort)},
		{Name: envBeruHTTPURL, Value: fmt.Sprintf("http://%s", beruHTTPHostFor(st, shadowNS))},
		{Name: envShadowTestName, Value: st.Name},
	}
	shopEnv = append(shopEnv, storageEnvVars(st, sessionID)...)

	_, err := ctrl.CreateOrPatch(ctx, r.Client, deploy, func() error {
		deploy.Labels = labels
		deploy.Spec.Replicas = &replicas
		deploy.Spec.Selector = &metav1.LabelSelector{MatchLabels: labels}
		deploy.Spec.Template.ObjectMeta.Labels = labels
		deploy.Spec.Template.Spec.Containers = []corev1.Container{{
			Name:            shopName,
			Image:           shopImageFor(st),
			ImagePullPolicy: corev1.PullIfNotPresent,
			Ports: []corev1.ContainerPort{
				{Name: "grpc", ContainerPort: shopGRPCPort, Protocol: corev1.ProtocolTCP},
				{Name: "http", ContainerPort: shopHTTPPort, Protocol: corev1.ProtocolTCP},
			},
			Env: shopEnv,
			Resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("50m"),
					corev1.ResourceMemory: resource.MustParse("64Mi"),
				},
				Limits: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("200m"),
					corev1.ResourceMemory: resource.MustParse("128Mi"),
				},
			},
		}}
		return nil
	})
	return err
}

func (r *ShadowTestReconciler) shopDeploymentReady(
	ctx context.Context,
	shadowNS string,
) (bool, workloadWaitReason, error) {
	return r.deploymentBootReady(ctx, shadowNS, shopServiceName(), "Shop")
}
