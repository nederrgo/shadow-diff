package controller

import (
	"context"
	"fmt"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	enginev1alpha1 "github.com/shadow-diff/monarch/api/v1alpha1"
)

func dependencyResourceName(depName, role string) string {
	return sanitizeForDNS(fmt.Sprintf("%s-%s", depName, role))
}

func dependencyEndpoint(shadowNS, depName, role string, port int32) string {
	host := shadowServiceHost(shadowNS, dependencyResourceName(depName, role))
	return fmt.Sprintf("%s:%d", host, port)
}

func dependencyPodLabels(st *enginev1alpha1.ShadowTest, dep enginev1alpha1.DependencySpec, role string) map[string]string {
	labels := deploymentPodLabels(st, role)
	labels[labelDependencyName] = sanitizeForDNS(dep.Name)
	labels[labelResourceKind] = valueResourceKindDep
	return labels
}

func validateDependencies(st *enginev1alpha1.ShadowTest) error {
	seen := map[string]struct{}{}
	var mongoCount int
	for i, dep := range st.Spec.Dependencies {
		if dep.Name == "" {
			return fmt.Errorf("dependencies[%d]: name is required", i)
		}
		if dep.Type == "" {
			return fmt.Errorf("dependencies[%d] %q: type is required", i, dep.Name)
		}
		image, port := resolveDependencyDefaults(dep)
		if image == "" {
			return fmt.Errorf("dependencies[%d] %q: unknown type %q (image required)", i, dep.Name, dep.Type)
		}
		if dep.EnvVarInjection == "" {
			return fmt.Errorf("dependencies[%d] %q: envVarInjection is required", i, dep.Name)
		}
		if port < 1 || port > 65535 {
			return fmt.Errorf("dependencies[%d] %q: port %d out of range", i, dep.Name, port)
		}
		sanitized := sanitizeForDNS(dep.Name)
		if sanitized == "" {
			return fmt.Errorf("dependencies[%d] %q: name is not a valid DNS label after sanitization", i, dep.Name)
		}
		if _, ok := seen[sanitized]; ok {
			return fmt.Errorf("duplicate dependency name %q after sanitization", dep.Name)
		}
		seen[sanitized] = struct{}{}
		if port == mongoProxyPort {
			if mongoCount++; mongoCount > 1 {
				return fmt.Errorf("only one dependency on port %d (MongoDB) is supported", mongoProxyPort)
			}
		}

		for _, role := range []string{roleControlA, roleControlB, roleCandidate} {
			resName := dependencyResourceName(dep.Name, role)
			if resName == shadowDeploymentName(st, role) || resName == igrisDeploymentName(st) {
				return fmt.Errorf("dependency %q collides with shadow workload name %q", dep.Name, resName)
			}
			if needsEgressRelayRabbitMQ(st) && resName == egressRelayRabbitMQDeploymentName(st) {
				return fmt.Errorf("dependency %q collides with egress-relay-rabbitmq name %q", dep.Name, resName)
			}
		}
	}
	return nil
}

func (r *ShadowTestReconciler) reconcileShadowDependencies(
	ctx context.Context,
	st *enginev1alpha1.ShadowTest,
	shadowNS string,
) error {
	if err := r.reconcileRabbitMQBrokerConfigMaps(ctx, st, shadowNS); err != nil {
		return err
	}
	desired := map[string]struct{}{}
	for _, dep := range st.Spec.Dependencies {
		for _, role := range []string{roleControlA, roleControlB, roleCandidate} {
			name := dependencyResourceName(dep.Name, role)
			desired[name] = struct{}{}
			if err := r.reconcileDependencyDeployment(ctx, st, shadowNS, dep, role); err != nil {
				return err
			}
			if err := r.reconcileDependencyService(ctx, st, shadowNS, dep, role); err != nil {
				return err
			}
		}
	}
	return r.pruneShadowDependencies(ctx, st, shadowNS, desired)
}

func (r *ShadowTestReconciler) reconcileDependencyDeployment(
	ctx context.Context,
	st *enginev1alpha1.ShadowTest,
	shadowNS string,
	dep enginev1alpha1.DependencySpec,
	role string,
) error {
	name := dependencyResourceName(dep.Name, role)
	podLabels := dependencyPodLabels(st, dep, role)
	image, port := resolveDependencyDefaults(dep)

	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: shadowNS,
			Name:      name,
		},
	}
	replicas := int32(1)
	_, err := ctrl.CreateOrPatch(ctx, r.Client, deploy, func() error {
		if deploy.Labels == nil {
			deploy.Labels = map[string]string{}
		}
		for k, v := range podLabels {
			deploy.Labels[k] = v
		}
		deploy.Spec.Replicas = &replicas
		deploy.Spec.Selector = &metav1.LabelSelector{MatchLabels: podLabels}
		deploy.Spec.Template.ObjectMeta.Labels = podLabels
		if isRabbitMQBrokerDependency(dep) {
			deploy.Spec.Template.Spec = rabbitMQBrokerPodSpec(dep, image, port)
		} else {
			deploy.Spec.Template.Spec.Containers = []corev1.Container{{
				Name:            "dependency",
				Image:           image,
				ImagePullPolicy: corev1.PullIfNotPresent,
				Ports: []corev1.ContainerPort{{
					Name:          "tcp",
					ContainerPort: port,
					Protocol:      corev1.ProtocolTCP,
				}},
				ReadinessProbe: &corev1.Probe{
					ProbeHandler: corev1.ProbeHandler{
						TCPSocket: &corev1.TCPSocketAction{Port: intstr.FromInt32(port)},
					},
					InitialDelaySeconds: 2,
					PeriodSeconds:       5,
				},
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("50m"),
						corev1.ResourceMemory: resource.MustParse("64Mi"),
					},
					Limits: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("200m"),
						corev1.ResourceMemory: resource.MustParse("256Mi"),
					},
				},
			}}
		}
		return nil
	})
	return err
}

func (r *ShadowTestReconciler) reconcileDependencyService(
	ctx context.Context,
	st *enginev1alpha1.ShadowTest,
	shadowNS string,
	dep enginev1alpha1.DependencySpec,
	role string,
) error {
	name := dependencyResourceName(dep.Name, role)
	podLabels := dependencyPodLabels(st, dep, role)
	_, port := resolveDependencyDefaults(dep)

	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: shadowNS,
			Name:      name,
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
		svc.Spec.Ports = []corev1.ServicePort{{
			Name:       "tcp",
			Port:       port,
			TargetPort: intstr.FromInt32(port),
			Protocol:   corev1.ProtocolTCP,
		}}
		return nil
	})
	return err
}

func (r *ShadowTestReconciler) pruneShadowDependencies(
	ctx context.Context,
	st *enginev1alpha1.ShadowTest,
	shadowNS string,
	desired map[string]struct{},
) error {
	selector := client.MatchingLabels{
		labelShadowTestUID: string(st.UID),
		labelResourceKind:  valueResourceKindDep,
	}

	var deploys appsv1.DeploymentList
	if err := r.List(ctx, &deploys, client.InNamespace(shadowNS), selector); err != nil {
		return err
	}
	for i := range deploys.Items {
		if _, ok := desired[deploys.Items[i].Name]; !ok {
			if err := r.Delete(ctx, &deploys.Items[i]); err != nil && !apierrors.IsNotFound(err) {
				return err
			}
		}
	}

	var svcs corev1.ServiceList
	if err := r.List(ctx, &svcs, client.InNamespace(shadowNS), selector); err != nil {
		return err
	}
	for i := range svcs.Items {
		if _, ok := desired[svcs.Items[i].Name]; !ok {
			if err := r.Delete(ctx, &svcs.Items[i]); err != nil && !apierrors.IsNotFound(err) {
				return err
			}
		}
	}
	return nil
}

func (r *ShadowTestReconciler) shadowDependenciesReady(
	ctx context.Context,
	st *enginev1alpha1.ShadowTest,
	shadowNS string,
) (bool, error) {
	for _, dep := range st.Spec.Dependencies {
		for _, role := range []string{roleControlA, roleControlB, roleCandidate} {
			var deploy appsv1.Deployment
			key := client.ObjectKey{
				Namespace: shadowNS,
				Name:      dependencyResourceName(dep.Name, role),
			}
			if err := r.Get(ctx, key, &deploy); err != nil {
				if apierrors.IsNotFound(err) {
					return false, nil
				}
				return false, err
			}
			if deploy.Status.AvailableReplicas < 1 {
				return false, nil
			}
		}
	}
	return true, nil
}

func dependencyEnvValue(shadowNS string, dep enginev1alpha1.DependencySpec, role string) string {
	_, port := resolveDependencyDefaults(dep)
	if usesMongoProxyInjection(dep) {
		return shadowMongoProxyURL
	}
	if usesAMQPURLInjection(dep.EnvVarInjection) {
		return shadowAMQPURL(shadowNS, dep.Name, role, port)
	}
	return dependencyEndpoint(shadowNS, dep.Name, role, port)
}

func usesMongoProxyInjection(dep enginev1alpha1.DependencySpec) bool {
	return isMongoDependency(dep)
}

func usesAMQPURLInjection(envName string) bool {
	switch strings.ToUpper(strings.TrimSpace(envName)) {
	case "AMQP_URL", "RABBITMQ_URL", "RABBITMQ_AMQP_URL":
		return true
	default:
		return false
	}
}

func dependencyEnvVarsForRole(st *enginev1alpha1.ShadowTest, shadowNS, role string) []corev1.EnvVar {
	var out []corev1.EnvVar
	for _, dep := range st.Spec.Dependencies {
		out = append(out, corev1.EnvVar{
			Name:  dep.EnvVarInjection,
			Value: dependencyEnvValue(shadowNS, dep, role),
		})
	}
	return out
}
