package controller

import (
	"context"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	enginev1alpha1 "github.com/shadow-diff/monarch/api/v1alpha1"
)

const (
	localBeruName         = "beru-local"
	localBeruGRPCPort     = int32(50051)
	localBeruOTLPPort     = int32(4317)
	localBeruHTTPPort     = int32(8080)
	localBeruSQLiteSizeMi = int64(64)
	localBeruBootTimeout  = 10 * time.Minute
)

var terminalBeruPodWaitingReasons = map[string]struct{}{
	"ImagePullBackOff":           {},
	"ErrImagePull":               {},
	"CrashLoopBackOff":           {},
	"CreateContainerConfigError": {},
	"InvalidImageName":           {},
	"ErrImageNeverPull":          {},
}

type beruWaitReason struct {
	terminal bool
	message  string
}

func usesLocalBeru(st *enginev1alpha1.ShadowTest) bool {
	return st != nil && st.Spec.BeruGRPCAddress == ""
}

func localBeruLabels(st *enginev1alpha1.ShadowTest) map[string]string {
	return map[string]string{
		labelManagedBy:           valueManagedBy,
		labelShadowTestName:      st.Name,
		labelShadowTestCRNS:      st.Namespace,
		labelShadowTestUID:       string(st.UID),
		"app":                    localBeruName,
		"app.kubernetes.io/name": localBeruName,
	}
}

func localBeruDNSHost(shadowNS string) string {
	return shadowServiceHost(shadowNS, localBeruName)
}

func (r *ShadowTestReconciler) reconcileLocalBeruIfNeeded(
	ctx context.Context,
	st *enginev1alpha1.ShadowTest,
	shadowNS string,
) error {
	if !usesLocalBeru(st) {
		return nil
	}
	return r.reconcileLocalBeru(ctx, st, shadowNS)
}

func (r *ShadowTestReconciler) reconcileLocalBeru(
	ctx context.Context,
	st *enginev1alpha1.ShadowTest,
	shadowNS string,
) error {
	podLabels := localBeruLabels(st)

	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: shadowNS,
			Name:      localBeruName,
		},
	}
	if _, err := ctrl.CreateOrPatch(ctx, r.Client, svc, func() error {
		svc.Labels = podLabels
		svc.Spec.Selector = map[string]string{"app": localBeruName}
		svc.Spec.Ports = []corev1.ServicePort{
			{Name: "grpc", Port: localBeruGRPCPort, TargetPort: intstr.FromInt32(localBeruGRPCPort), Protocol: corev1.ProtocolTCP},
			{Name: "otlp-grpc", Port: localBeruOTLPPort, TargetPort: intstr.FromInt32(localBeruOTLPPort), Protocol: corev1.ProtocolTCP},
			{Name: "http", Port: localBeruHTTPPort, TargetPort: intstr.FromInt32(localBeruHTTPPort), Protocol: corev1.ProtocolTCP},
		}
		return nil
	}); err != nil {
		return err
	}

	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: shadowNS,
			Name:      localBeruName,
		},
	}
	replicas := int32(1)
	sqliteSizeLimit := resource.NewQuantity(localBeruSQLiteSizeMi*1024*1024, resource.BinarySI)
	_, err := ctrl.CreateOrPatch(ctx, r.Client, deploy, func() error {
		deploy.Labels = podLabels
		deploy.Spec.Replicas = &replicas
		deploy.Spec.Selector = &metav1.LabelSelector{MatchLabels: map[string]string{"app": localBeruName}}
		deploy.Spec.Template.ObjectMeta.Labels = map[string]string{"app": localBeruName}
		for k, v := range podLabels {
			deploy.Spec.Template.ObjectMeta.Labels[k] = v
		}
		deploy.Spec.Template.Spec.Containers = []corev1.Container{{
			Name:            "beru",
			Image:           beruImageFor(st),
			ImagePullPolicy: corev1.PullIfNotPresent,
			Ports: []corev1.ContainerPort{
				{Name: "grpc", ContainerPort: localBeruGRPCPort, Protocol: corev1.ProtocolTCP},
				{Name: "otlp-grpc", ContainerPort: localBeruOTLPPort, Protocol: corev1.ProtocolTCP},
				{Name: "http", ContainerPort: localBeruHTTPPort, Protocol: corev1.ProtocolTCP},
			},
			Env: []corev1.EnvVar{
				{Name: "BERU_GRPC_ADDR", Value: ":50051"},
				{Name: "BERU_OTLP_GRPC_ADDR", Value: ":4317"},
				{Name: "BERU_HTTP_ADDR", Value: ":8080"},
				{Name: "BERU_DB_PATH", Value: "/data/beru.db"},
			},
			Resources: corev1.ResourceRequirements{
				Limits: corev1.ResourceList{
					corev1.ResourceMemory: resource.MustParse("128Mi"),
					corev1.ResourceCPU:    resource.MustParse("200m"),
				},
			},
			VolumeMounts: []corev1.VolumeMount{{
				Name:      volumeNameLocalBeruData,
				MountPath: "/data",
			}},
		}}
		deploy.Spec.Template.Spec.Volumes = []corev1.Volume{{
			Name: volumeNameLocalBeruData,
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{
					Medium:    corev1.StorageMediumMemory,
					SizeLimit: sqliteSizeLimit,
				},
			},
		}}
		return nil
	})
	return err
}

func (r *ShadowTestReconciler) localBeruReady(
	ctx context.Context,
	shadowNS string,
) (bool, beruWaitReason, error) {
	var deploy appsv1.Deployment
	key := client.ObjectKey{Namespace: shadowNS, Name: localBeruName}
	if err := r.Get(ctx, key, &deploy); err != nil {
		if apierrors.IsNotFound(err) {
			return false, beruWaitReason{}, nil
		}
		return false, beruWaitReason{}, err
	}
	if deploy.Status.AvailableReplicas > 0 {
		return true, beruWaitReason{}, nil
	}
	if reason := beruDeploymentTerminalReason(&deploy); reason.terminal {
		return false, reason, nil
	}
	if reason := r.beruPodTerminalReason(ctx, shadowNS, deploy.Spec.Selector); reason.terminal {
		return false, reason, nil
	}
	if !deploy.CreationTimestamp.IsZero() && time.Since(deploy.CreationTimestamp.Time) > localBeruBootTimeout {
		return false, beruWaitReason{
			terminal: true,
			message:  fmt.Sprintf("beru-local did not become ready within %s", localBeruBootTimeout),
		}, nil
	}
	return false, beruWaitReason{}, nil
}

func beruDeploymentTerminalReason(deploy *appsv1.Deployment) beruWaitReason {
	for _, c := range deploy.Status.Conditions {
		if c.Type == "ProgressDeadlineExceeded" && c.Status == corev1.ConditionTrue {
			msg := c.Message
			if msg == "" {
				msg = "beru-local deployment progress deadline exceeded"
			}
			return beruWaitReason{terminal: true, message: msg}
		}
	}
	return beruWaitReason{}
}

func (r *ShadowTestReconciler) beruPodTerminalReason(
	ctx context.Context,
	shadowNS string,
	selector *metav1.LabelSelector,
) beruWaitReason {
	if selector == nil {
		return beruWaitReason{}
	}
	sel, err := metav1.LabelSelectorAsSelector(selector)
	if err != nil {
		return beruWaitReason{}
	}
	var pods corev1.PodList
	if err := r.List(ctx, &pods, client.InNamespace(shadowNS), client.MatchingLabelsSelector{Selector: sel}); err != nil {
		return beruWaitReason{}
	}
	for i := range pods.Items {
		if reason := podTerminalReason(&pods.Items[i]); reason.terminal {
			return reason
		}
	}
	return beruWaitReason{}
}

func podTerminalReason(pod *corev1.Pod) beruWaitReason {
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.State.Waiting != nil {
			if _, ok := terminalBeruPodWaitingReasons[cs.State.Waiting.Reason]; ok {
				return beruWaitReason{
					terminal: true,
					message:  fmt.Sprintf("beru-local pod %s: %s (%s)", pod.Name, cs.State.Waiting.Reason, cs.State.Waiting.Message),
				}
			}
		}
		if cs.State.Terminated != nil && cs.State.Terminated.ExitCode != 0 {
			return beruWaitReason{
				terminal: true,
				message:  fmt.Sprintf("beru-local pod %s container %s exited %d: %s",
					pod.Name, cs.Name, cs.State.Terminated.ExitCode, cs.State.Terminated.Message),
			}
		}
	}
	return beruWaitReason{}
}

// ponytail: used only in tests to assert label selector wiring
func localBeruPodSelector() labels.Selector {
	sel, _ := metav1.LabelSelectorAsSelector(&metav1.LabelSelector{
		MatchLabels: map[string]string{"app": localBeruName},
	})
	return sel
}
