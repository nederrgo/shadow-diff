package controller

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"

	enginev1alpha1 "github.com/shadow-diff/monarch/api/v1alpha1"
)

const (
	containerIgrisRabbitMQ       = "igris-rabbitmq"
	envProdURL                   = "PROD_URL"
	envShadowQueueName           = "SHADOW_QUEUE_NAME"
	envShadowPublishExchange     = "SHADOW_PUBLISH_EXCHANGE"
	envShadowPublishExchangeType = "SHADOW_PUBLISH_EXCHANGE_TYPE"
	envControlAAMQPURL           = "CONTROL_A_AMQP_URL"
	envControlBAMQPURL           = "CONTROL_B_AMQP_URL"
	envCandidateAMQPURL          = "CANDIDATE_AMQP_URL"
	envSamplePercentage          = "IGRIS_RMQ_SAMPLE_PERCENTAGE"
	defaultAMQPUser              = "guest"
	defaultAMQPPass              = "guest"
)

func igrisRabbitMQDeploymentName(st *enginev1alpha1.ShadowTest) string {
	return sanitizeForDNS(fmt.Sprintf("%s-igris-rabbitmq", st.Name))
}

func igrisRabbitMQServiceName(st *enginev1alpha1.ShadowTest) string {
	return igrisRabbitMQDeploymentName(st)
}

func igrisRabbitMQReplicasFor(st *enginev1alpha1.ShadowTest) int32 {
	if st.Spec.IgrisRabbitMQ != nil && st.Spec.IgrisRabbitMQ.Replicas != nil && *st.Spec.IgrisRabbitMQ.Replicas > 0 {
		return *st.Spec.IgrisRabbitMQ.Replicas
	}
	return 1
}

func shadowAMQPURL(shadowNS, depName, role string, port int32) string {
	host := shadowServiceHost(shadowNS, dependencyResourceName(depName, role))
	return fmt.Sprintf("amqp://%s:%s@%s:%d/", defaultAMQPUser, defaultAMQPPass, host, port)
}

func (r *ShadowTestReconciler) igrisRabbitMQEnv(ctx context.Context, st *enginev1alpha1.ShadowTest, shadowNS string) ([]corev1.EnvVar, error) {
	amqpSpec, err := firstAMQPInput(st)
	if err != nil {
		return nil, err
	}
	dep, ok := dependencyByName(st, amqpSpec.TargetDependency)
	if !ok {
		return nil, fmt.Errorf("dependency %q not found", amqpSpec.TargetDependency)
	}
	_, port := resolveDependencyDefaults(*dep)
	mode := operatingMode(st)
	sessionID := strings.TrimSpace(st.Status.CurrentSessionID)

	env := []corev1.EnvVar{
		{Name: envShadowPublishExchange, Value: amqpSpec.Exchange},
		{Name: envShadowPublishExchangeType, Value: amqpExchangeType(amqpSpec)},
		{Name: envSamplePercentage, Value: strconv.Itoa(samplePercentage(st))},
		{Name: envIgrisAdminAddr, Value: defaultIgrisAdminAddr},
	}
	env = append(env, storageEnvVars(st, sessionID)...)

	switch mode {
	case modeRecord:
		queueName := st.Status.AmqpQueueName
		if queueName == "" {
			queueName = prodShadowQueueName(st)
		}
		prodURL, err := r.resolveProdAMQPURL(ctx, st, amqpSpec)
		if err != nil {
			return nil, err
		}
		env = append(env,
			corev1.EnvVar{Name: envProdURL, Value: prodURL},
			corev1.EnvVar{Name: envShadowQueueName, Value: queueName},
		)
		// ponytail: record dials prod only — omit shadow broker URLs so startup does not fail when deps are absent.
	case modeReplay:
		env = append(env,
			corev1.EnvVar{Name: envControlAAMQPURL, Value: shadowAMQPURL(shadowNS, dep.Name, roleControlA, port)},
			corev1.EnvVar{Name: envControlBAMQPURL, Value: shadowAMQPURL(shadowNS, dep.Name, roleControlB, port)},
			corev1.EnvVar{Name: envCandidateAMQPURL, Value: shadowAMQPURL(shadowNS, dep.Name, roleCandidate, port)},
		)
	}
	return env, nil
}

func (r *ShadowTestReconciler) reconcileIgrisRabbitMQDeployment(
	ctx context.Context,
	st *enginev1alpha1.ShadowTest,
	shadowNS string,
) error {
	if !hasRabbitMQInput(st) {
		return nil
	}
	if operatingMode(st) == modeRecord && st.Status.AmqpQueueName == "" {
		return fmt.Errorf("cannot deploy igris-rabbitmq before prod shadow queue is provisioned")
	}

	name := igrisRabbitMQDeploymentName(st)
	labels := map[string]string{
		labelManagedBy:           valueManagedBy,
		labelShadowTestName:      st.Name,
		labelShadowTestCRNS:      st.Namespace,
		labelShadowTestUID:       string(st.UID),
		"app.kubernetes.io/name": containerIgrisRabbitMQ,
	}
	env, err := r.igrisRabbitMQEnv(ctx, st, shadowNS)
	if err != nil {
		return err
	}

	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: shadowNS,
			Name:      name,
		},
	}
	replicas := igrisRabbitMQReplicasFor(st)
	_, err = ctrl.CreateOrPatch(ctx, r.Client, deploy, func() error {
		deploy.Labels = labels
		deploy.Spec.Replicas = &replicas
		deploy.Spec.Selector = &metav1.LabelSelector{MatchLabels: labels}
		deploy.Spec.Template.ObjectMeta.Labels = labels

		container := corev1.Container{
			Name:            containerIgrisRabbitMQ,
			Image:           igrisRabbitMQImageFor(st),
			ImagePullPolicy: corev1.PullIfNotPresent,
			Ports: []corev1.ContainerPort{{
				Name:          "admin",
				ContainerPort: igrisAdminPort,
				Protocol:      corev1.ProtocolTCP,
			}},
			Env: env,
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
		if st.Spec.IgrisRabbitMQ != nil && st.Spec.IgrisRabbitMQ.Resources != nil {
			container.Resources = *st.Spec.IgrisRabbitMQ.Resources
		}
		deploy.Spec.Template.Spec.Containers = []corev1.Container{container}
		return nil
	})
	return err
}

func (r *ShadowTestReconciler) reconcileIgrisRabbitMQService(
	ctx context.Context,
	st *enginev1alpha1.ShadowTest,
	shadowNS string,
) error {
	if !hasRabbitMQInput(st) {
		return nil
	}
	name := igrisRabbitMQServiceName(st)
	labels := map[string]string{
		labelManagedBy:           valueManagedBy,
		labelShadowTestName:      st.Name,
		"app.kubernetes.io/name": containerIgrisRabbitMQ,
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
				Name:       "admin",
				Port:       igrisAdminPort,
				TargetPort: intstr.FromInt32(igrisAdminPort),
				Protocol:   corev1.ProtocolTCP,
			},
		}
		return nil
	})
	return err
}

func (r *ShadowTestReconciler) igrisRabbitMQDeploymentReady(
	ctx context.Context,
	st *enginev1alpha1.ShadowTest,
	shadowNS string,
) (bool, workloadWaitReason, error) {
	if !hasRabbitMQInput(st) {
		return true, workloadWaitReason{}, nil
	}
	return r.deploymentBootReady(ctx, shadowNS, igrisRabbitMQDeploymentName(st), "igris-rabbitmq")
}

func (r *ShadowTestReconciler) reconcileIgrisRabbitMQStack(
	ctx context.Context,
	st *enginev1alpha1.ShadowTest,
	shadowNS string,
) error {
	if err := r.reconcileIgrisRabbitMQDeployment(ctx, st, shadowNS); err != nil {
		return err
	}
	return r.reconcileIgrisRabbitMQService(ctx, st, shadowNS)
}
