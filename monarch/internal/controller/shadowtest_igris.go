package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	enginev1alpha1 "github.com/shadow-diff/monarch/api/v1alpha1"
)

func igrisDeploymentName(st *enginev1alpha1.ShadowTest) string {
	return sanitizeForDNS(fmt.Sprintf("%s-igris", st.Name))
}

func igrisConfigMapName(st *enginev1alpha1.ShadowTest) string {
	return sanitizeForDNS(fmt.Sprintf("%s-igris-config", st.Name))
}

func igrisServiceName(st *enginev1alpha1.ShadowTest) string {
	return igrisDeploymentName(st)
}

func shadowDeploymentName(st *enginev1alpha1.ShadowTest, role string) string {
	return sanitizeForDNS(fmt.Sprintf("%s-%s", st.Name, role))
}

func igrisListenersJSON(st *enginev1alpha1.ShadowTest) (string, error) {
	type listener struct {
		Port   int32  `json:"port"`
		Driver string `json:"driver"`
	}
	inputs := resolvedInputs(st)
	out := make([]listener, len(inputs))
	for i, in := range inputs {
		out[i] = listener{Port: in.Port, Driver: in.Driver}
	}
	b, err := json.Marshal(out)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func shadowServiceURL(shadowNS, serviceName string, port int32) string {
	host := fmt.Sprintf("%s.%s.svc.cluster.local", serviceName, shadowNS)
	return fmt.Sprintf("http://%s:%d", host, port)
}

func igrisImageFor(st *enginev1alpha1.ShadowTest) string {
	if st.Spec.Igris != nil && st.Spec.Igris.Image != "" {
		return st.Spec.Igris.Image
	}
	return defaultIgrisImage
}

func igrisReplicasFor(st *enginev1alpha1.ShadowTest) int32 {
	if st.Spec.Igris != nil && st.Spec.Igris.Replicas != nil && *st.Spec.Igris.Replicas > 0 {
		return *st.Spec.Igris.Replicas
	}
	return 1
}

func (r *ShadowTestReconciler) reconcileShadowService(
	ctx context.Context,
	st *enginev1alpha1.ShadowTest,
	shadowNS, role string,
) error {
	deployName := shadowDeploymentName(st, role)
	podLabels := deploymentPodLabels(st, role)
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: shadowNS,
			Name:      deployName,
		},
	}
	_, err := ctrl.CreateOrPatch(ctx, r.Client, svc, func() error {
		if svc.Labels == nil {
			svc.Labels = map[string]string{}
		}
		for k, v := range podLabels {
			svc.Labels[k] = v
		}
		svc.Spec.Selector = podLabels
		svc.Spec.Ports = shadowServicePorts(st)
		return nil
	})
	return err
}

func (r *ShadowTestReconciler) reconcileIgrisConfigMap(
	ctx context.Context,
	st *enginev1alpha1.ShadowTest,
	shadowNS string,
) error {
	listeners, err := igrisListenersJSON(st)
	if err != nil {
		return err
	}
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: shadowNS,
			Name:      igrisConfigMapName(st),
		},
	}
	_, err = ctrl.CreateOrPatch(ctx, r.Client, cm, func() error {
		if cm.Labels == nil {
			cm.Labels = map[string]string{}
		}
		cm.Labels[labelManagedBy] = valueManagedBy
		cm.Labels[labelShadowTestName] = st.Name
		if cm.Data == nil {
			cm.Data = map[string]string{}
		}
		cm.Data[configMapKeyListenersJSON] = listeners
		return nil
	})
	return err
}

func (r *ShadowTestReconciler) reconcileIgrisDeployment(
	ctx context.Context,
	st *enginev1alpha1.ShadowTest,
	shadowNS string,
) error {
	name := igrisDeploymentName(st)
	labels := map[string]string{
		labelManagedBy:           valueManagedBy,
		labelShadowTestName:      st.Name,
		labelShadowTestCRNS:      st.Namespace,
		labelShadowTestUID:       string(st.UID),
		"app.kubernetes.io/name": "igris",
	}
	inputs := resolvedInputs(st)
	var containerPorts []corev1.ContainerPort
	for _, in := range inputs {
		containerPorts = append(containerPorts, corev1.ContainerPort{
			Name:          inputPortName(in.Driver, in.Port),
			ContainerPort: in.Port,
			Protocol:      corev1.ProtocolTCP,
		})
	}

	controlAURL, controlBURL, candidateURL := igrisControlURLs(st, shadowNS)
	controlAAddr, controlBAddr, candidateAddr := igrisControlHosts(st, shadowNS)

	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: shadowNS,
			Name:      name,
		},
	}
	replicas := igrisReplicasFor(st)
	_, err := ctrl.CreateOrPatch(ctx, r.Client, deploy, func() error {
		deploy.Labels = labels
		deploy.Spec.Replicas = &replicas
		deploy.Spec.Selector = &metav1.LabelSelector{MatchLabels: labels}
		deploy.Spec.Template.ObjectMeta.Labels = labels
		termination := igrisTerminationGraceSeconds
		deploy.Spec.Template.Spec.TerminationGracePeriodSeconds = &termination

		container := corev1.Container{
			Name:            containerIgris,
			Image:           igrisImageFor(st),
			ImagePullPolicy: corev1.PullIfNotPresent,
			Ports:           containerPorts,
			Env: []corev1.EnvVar{
				{Name: envControlAURL, Value: controlAURL},
				{Name: envControlBURL, Value: controlBURL},
				{Name: envCandidateURL, Value: candidateURL},
				{Name: envControlAAddr, Value: controlAAddr},
				{Name: envControlBAddr, Value: controlBAddr},
				{Name: envCandidateAddr, Value: candidateAddr},
				{Name: envIgrisListenersFile, Value: defaultIgrisListenersPath},
			},
			VolumeMounts: []corev1.VolumeMount{
				{Name: volumeNameIgrisConfig, MountPath: "/etc/igris", ReadOnly: true},
			},
		}
		if st.Spec.Igris != nil && st.Spec.Igris.Resources != nil {
			container.Resources = *st.Spec.Igris.Resources
		} else {
			container.Resources = corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("50m"),
					corev1.ResourceMemory: resource.MustParse("64Mi"),
				},
				Limits: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("500m"),
					corev1.ResourceMemory: resource.MustParse("256Mi"),
				},
			}
		}
		deploy.Spec.Template.Spec.Containers = []corev1.Container{container}
		deploy.Spec.Template.Spec.Volumes = []corev1.Volume{{
			Name: volumeNameIgrisConfig,
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{Name: igrisConfigMapName(st)},
				},
			},
		}}
		return nil
	})
	return err
}

func (r *ShadowTestReconciler) reconcileIgrisService(
	ctx context.Context,
	st *enginev1alpha1.ShadowTest,
	shadowNS string,
) error {
	name := igrisServiceName(st)
	labels := map[string]string{
		labelManagedBy:           valueManagedBy,
		labelShadowTestName:      st.Name,
		"app.kubernetes.io/name": "igris",
	}
	inputs := resolvedInputs(st)
	var ports []corev1.ServicePort
	for _, in := range inputs {
		ports = append(ports, corev1.ServicePort{
			Name:       inputPortName(in.Driver, in.Port),
			Port:       in.Port,
			TargetPort: intstr.FromInt32(in.Port),
			Protocol:   corev1.ProtocolTCP,
		})
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
		svc.Spec.Ports = ports
		return nil
	})
	return err
}

func (r *ShadowTestReconciler) shadowDeploymentsReady(
	ctx context.Context,
	st *enginev1alpha1.ShadowTest,
	shadowNS string,
) (bool, error) {
	for _, role := range []string{roleControlA, roleControlB, roleCandidate} {
		var deploy appsv1.Deployment
		key := client.ObjectKey{Namespace: shadowNS, Name: shadowDeploymentName(st, role)}
		if err := r.Get(ctx, key, &deploy); err != nil {
			return false, err
		}
		if deploy.Status.AvailableReplicas < 1 {
			return false, nil
		}
	}
	return true, nil
}

func (r *ShadowTestReconciler) igrisDeploymentReady(
	ctx context.Context,
	st *enginev1alpha1.ShadowTest,
	shadowNS string,
) (bool, error) {
	var deploy appsv1.Deployment
	key := client.ObjectKey{Namespace: shadowNS, Name: igrisDeploymentName(st)}
	if err := r.Get(ctx, key, &deploy); err != nil {
		return false, err
	}
	return deploy.Status.AvailableReplicas > 0, nil
}

func igrisControlURLs(st *enginev1alpha1.ShadowTest, shadowNS string) (string, string, string) {
	return shadowServiceURL(shadowNS, shadowDeploymentName(st, roleControlA), st.Spec.ServicePort),
		shadowServiceURL(shadowNS, shadowDeploymentName(st, roleControlB), st.Spec.ServicePort),
		shadowServiceURL(shadowNS, shadowDeploymentName(st, roleCandidate), st.Spec.ServicePort)
}

// listenersSummary returns a short human-readable listener description.
func listenersSummary(st *enginev1alpha1.ShadowTest) string {
	var parts []string
	for _, in := range resolvedInputs(st) {
		parts = append(parts, fmt.Sprintf("%d:%s", in.Port, in.Driver))
	}
	return strings.Join(parts, ",")
}
