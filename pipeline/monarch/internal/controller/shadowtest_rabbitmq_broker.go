package controller

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	enginev1alpha1 "github.com/shadow-diff/monarch/api/v1alpha1"
)

const (
	rabbitmqPluginsConfigKey      = "enabled_plugins"
	rabbitmqPluginsMountPath      = "/custom-config/enabled_plugins"
	envRabbitMQEnabledPluginsFile = "RABBITMQ_ENABLED_PLUGINS_FILE"
	volumeNameRabbitMQPlugins     = "rabbitmq-plugins"
	rabbitmqEnabledPluginsContent = "[rabbitmq_management,rabbitmq_tracing]."
	// Marker lives under /tmp to avoid permission conflicts with RabbitMQ data dir.
	rabbitmqFirehoseReadyMarker      = "/tmp/.firehose_ready"
	rabbitmqFirehoseReadinessCommand = `rabbitmq-diagnostics -q check_running && { test -f ` + rabbitmqFirehoseReadyMarker + ` || { rabbitmqctl trace_on && touch ` + rabbitmqFirehoseReadyMarker + `; }; }`
)

func isRabbitMQBrokerDependency(dep enginev1alpha1.DependencySpec) bool {
	return usesAMQPURLInjection(dep.EnvVarInjection)
}

func rabbitmqPluginsConfigMapName(depName string) string {
	return sanitizeForDNS(fmt.Sprintf("%s-rabbitmq-plugins", depName))
}

func (r *ShadowTestReconciler) reconcileRabbitMQBrokerConfigMaps(
	ctx context.Context,
	st *enginev1alpha1.ShadowTest,
	shadowNS string,
) error {
	desired := map[string]struct{}{}
	for _, dep := range st.Spec.Dependencies {
		if !isRabbitMQBrokerDependency(dep) {
			continue
		}
		name := rabbitmqPluginsConfigMapName(dep.Name)
		desired[name] = struct{}{}
		if err := r.reconcileRabbitMQPluginsConfigMap(ctx, st, shadowNS, dep); err != nil {
			return err
		}
	}
	return r.pruneRabbitMQBrokerConfigMaps(ctx, st, shadowNS, desired)
}

func (r *ShadowTestReconciler) reconcileRabbitMQPluginsConfigMap(
	ctx context.Context,
	st *enginev1alpha1.ShadowTest,
	shadowNS string,
	dep enginev1alpha1.DependencySpec,
) error {
	name := rabbitmqPluginsConfigMapName(dep.Name)
	labels := map[string]string{
		labelManagedBy:      valueManagedBy,
		labelShadowTestName: st.Name,
		labelShadowTestCRNS: st.Namespace,
		labelShadowTestUID:  string(st.UID),
		labelDependencyName: sanitizeForDNS(dep.Name),
		labelResourceKind:   valueResourceKindDep,
	}
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: shadowNS,
			Name:      name,
		},
	}
	_, err := ctrl.CreateOrPatch(ctx, r.Client, cm, func() error {
		cm.Labels = labels
		if cm.Data == nil {
			cm.Data = map[string]string{}
		}
		cm.Data[rabbitmqPluginsConfigKey] = rabbitmqEnabledPluginsContent
		return nil
	})
	return err
}

func (r *ShadowTestReconciler) pruneRabbitMQBrokerConfigMaps(
	ctx context.Context,
	st *enginev1alpha1.ShadowTest,
	shadowNS string,
	desired map[string]struct{},
) error {
	selector := client.MatchingLabels{
		labelShadowTestUID: string(st.UID),
		labelResourceKind:  valueResourceKindDep,
	}
	var cms corev1.ConfigMapList
	if err := r.List(ctx, &cms, client.InNamespace(shadowNS), selector); err != nil {
		return err
	}
	for i := range cms.Items {
		if _, ok := desired[cms.Items[i].Name]; !ok {
			if err := r.Delete(ctx, &cms.Items[i]); err != nil && !apierrors.IsNotFound(err) {
				return err
			}
		}
	}
	return nil
}

func rabbitMQBrokerContainer(dep enginev1alpha1.DependencySpec) corev1.Container {
	return corev1.Container{
		Name:            "dependency",
		Image:           dep.Image,
		ImagePullPolicy: corev1.PullIfNotPresent,
		Env: []corev1.EnvVar{{
			Name:  envRabbitMQEnabledPluginsFile,
			Value: rabbitmqPluginsMountPath,
		}},
		Ports: []corev1.ContainerPort{{
			Name:          "tcp",
			ContainerPort: dep.Port,
			Protocol:      corev1.ProtocolTCP,
		}},
		VolumeMounts: []corev1.VolumeMount{{
			Name:      volumeNameRabbitMQPlugins,
			MountPath: rabbitmqPluginsMountPath,
			SubPath:   rabbitmqPluginsConfigKey,
			ReadOnly:  true,
		}},
		ReadinessProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				TCPSocket: &corev1.TCPSocketAction{Port: intstr.FromInt32(dep.Port)},
			},
			PeriodSeconds:    5,
			TimeoutSeconds:   3,
			FailureThreshold: 3,
		},
		StartupProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				Exec: &corev1.ExecAction{
					Command: []string{"/bin/sh", "-c", rabbitmqFirehoseReadinessCommand},
				},
			},
			InitialDelaySeconds: 10,
			PeriodSeconds:       10,
			// ponytail: trace_on can exceed 30s on small clusters; upgrade path is split readiness from trace_on
			TimeoutSeconds:   60,
			FailureThreshold: 36,
		},
		Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("100m"),
				corev1.ResourceMemory: resource.MustParse("256Mi"),
			},
			Limits: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("500m"),
				corev1.ResourceMemory: resource.MustParse("512Mi"),
			},
		},
	}
}

func rabbitMQBrokerPodSpec(dep enginev1alpha1.DependencySpec) corev1.PodSpec {
	return corev1.PodSpec{
		Volumes: []corev1.Volume{{
			Name: volumeNameRabbitMQPlugins,
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: rabbitmqPluginsConfigMapName(dep.Name),
					},
				},
			},
		}},
		Containers: []corev1.Container{rabbitMQBrokerContainer(dep)},
	}
}
