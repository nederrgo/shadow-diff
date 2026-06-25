package controller

import (
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	enginev1alpha1 "github.com/shadow-diff/monarch/api/v1alpha1"
)

const containerEgressRelayRabbitMQ = "egress-relay-rabbitmq"

func egressRelayRabbitMQDeploymentName(st *enginev1alpha1.ShadowTest) string {
	return sanitizeForDNS(fmt.Sprintf("%s-egress-relay-rabbitmq", st.Name))
}

func egressRelayRabbitMQReplicasFor(st *enginev1alpha1.ShadowTest) int32 {
	if st.Spec.EgressRelayRabbitMQ != nil && st.Spec.EgressRelayRabbitMQ.Replicas != nil && *st.Spec.EgressRelayRabbitMQ.Replicas > 0 {
		return *st.Spec.EgressRelayRabbitMQ.Replicas
	}
	return 1
}

func hasRabbitMQBrokerDependency(st *enginev1alpha1.ShadowTest) bool {
	for _, dep := range st.Spec.Dependencies {
		if isRabbitMQBrokerDependency(dep) {
			return true
		}
	}
	return false
}

func needsEgressRelayRabbitMQ(st *enginev1alpha1.ShadowTest) bool {
	return hasRabbitMQInput(st) || hasRabbitMQBrokerDependency(st)
}

func rabbitMQBrokerDependencyForEgressRelay(st *enginev1alpha1.ShadowTest) (*enginev1alpha1.DependencySpec, error) {
	if hasRabbitMQInput(st) {
		amqpSpec, err := firstAMQPInput(st)
		if err != nil {
			return nil, err
		}
		dep, ok := dependencyByName(st, amqpSpec.TargetDependency)
		if !ok {
			return nil, fmt.Errorf("dependency %q not found", amqpSpec.TargetDependency)
		}
		return dep, nil
	}
	for i := range st.Spec.Dependencies {
		if isRabbitMQBrokerDependency(st.Spec.Dependencies[i]) {
			return &st.Spec.Dependencies[i], nil
		}
	}
	return nil, fmt.Errorf("no rabbitmq broker dependency found")
}

func (r *ShadowTestReconciler) egressRelayRabbitMQEnv(st *enginev1alpha1.ShadowTest, shadowNS string) ([]corev1.EnvVar, error) {
	dep, err := rabbitMQBrokerDependencyForEgressRelay(st)
	if err != nil {
		return nil, err
	}
	_, port := resolveDependencyDefaults(*dep)
	return []corev1.EnvVar{
		{Name: envControlAAMQPURL, Value: shadowAMQPURL(shadowNS, dep.Name, roleControlA, port)},
		{Name: envControlBAMQPURL, Value: shadowAMQPURL(shadowNS, dep.Name, roleControlB, port)},
		{Name: envCandidateAMQPURL, Value: shadowAMQPURL(shadowNS, dep.Name, roleCandidate, port)},
		{Name: envBeruHTTPURL, Value: fmt.Sprintf("http://%s", beruHTTPHostFor(st))},
		// ponytail: default worker egress exchange; skip igris ingress publishes on orders
		{Name: "EGRESS_EXCHANGE", Value: "egress-events"},
	}, nil
}

func (r *ShadowTestReconciler) reconcileEgressRelayRabbitMQDeployment(
	ctx context.Context,
	st *enginev1alpha1.ShadowTest,
	shadowNS string,
) error {
	if !needsEgressRelayRabbitMQ(st) {
		return nil
	}

	name := egressRelayRabbitMQDeploymentName(st)
	labels := map[string]string{
		labelManagedBy:           valueManagedBy,
		labelShadowTestName:      st.Name,
		labelShadowTestCRNS:      st.Namespace,
		labelShadowTestUID:       string(st.UID),
		"app.kubernetes.io/name": containerEgressRelayRabbitMQ,
	}
	env, err := r.egressRelayRabbitMQEnv(st, shadowNS)
	if err != nil {
		return err
	}

	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: shadowNS,
			Name:      name,
		},
	}
	replicas := egressRelayRabbitMQReplicasFor(st)
	_, err = ctrl.CreateOrPatch(ctx, r.Client, deploy, func() error {
		deploy.Labels = labels
		deploy.Spec.Replicas = &replicas
		deploy.Spec.Selector = &metav1.LabelSelector{MatchLabels: labels}
		deploy.Spec.Template.ObjectMeta.Labels = labels

		container := corev1.Container{
			Name:            containerEgressRelayRabbitMQ,
			Image:           egressRelayRabbitMQImageFor(st),
			ImagePullPolicy: corev1.PullIfNotPresent,
			Env:             env,
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
		if st.Spec.EgressRelayRabbitMQ != nil && st.Spec.EgressRelayRabbitMQ.Resources != nil {
			container.Resources = *st.Spec.EgressRelayRabbitMQ.Resources
		}
		deploy.Spec.Template.Spec.Containers = []corev1.Container{container}
		return nil
	})
	return err
}

func (r *ShadowTestReconciler) egressRelayRabbitMQDeploymentReady(
	ctx context.Context,
	st *enginev1alpha1.ShadowTest,
	shadowNS string,
) (bool, error) {
	if !needsEgressRelayRabbitMQ(st) {
		return true, nil
	}
	var deploy appsv1.Deployment
	key := client.ObjectKey{Namespace: shadowNS, Name: egressRelayRabbitMQDeploymentName(st)}
	if err := r.Get(ctx, key, &deploy); err != nil {
		return false, client.IgnoreNotFound(err)
	}
	return deploy.Status.AvailableReplicas > 0, nil
}

func (r *ShadowTestReconciler) reconcileEgressRelayRabbitMQStack(
	ctx context.Context,
	st *enginev1alpha1.ShadowTest,
	shadowNS string,
) error {
	return r.reconcileEgressRelayRabbitMQDeployment(ctx, st, shadowNS)
}
