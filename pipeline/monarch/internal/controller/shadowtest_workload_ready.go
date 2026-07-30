package controller

import (
	"context"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ponytail: 90s backstop; CrashLoop/ImagePull fail as soon as kubelet reports them.
const workloadBootTimeout = 90 * time.Second

var terminalPodWaitingReasons = map[string]struct{}{
	"ImagePullBackOff":           {},
	"ErrImagePull":               {},
	"CrashLoopBackOff":           {},
	"CreateContainerConfigError": {},
	"InvalidImageName":           {},
	"ErrImageNeverPull":          {},
}

// workloadWaitReason reports why a Deployment is not Ready.
type workloadWaitReason struct {
	terminal bool
	message  string
}

// deploymentBootReady is true when AvailableReplicas > 0. Otherwise checks
// ProgressDeadlineExceeded, terminal pod waiting/exit reasons, then boot timeout.
func (r *ShadowTestReconciler) deploymentBootReady(
	ctx context.Context,
	shadowNS, name, component string,
) (bool, workloadWaitReason, error) {
	var deploy appsv1.Deployment
	key := client.ObjectKey{Namespace: shadowNS, Name: name}
	if err := r.Get(ctx, key, &deploy); err != nil {
		if apierrors.IsNotFound(err) {
			return false, workloadWaitReason{}, nil
		}
		return false, workloadWaitReason{}, err
	}
	if deploy.Status.AvailableReplicas > 0 {
		return true, workloadWaitReason{}, nil
	}
	if reason := deploymentProgressTerminalReason(&deploy, component); reason.terminal {
		return false, reason, nil
	}
	if reason := r.podsTerminalReason(ctx, shadowNS, deploy.Spec.Selector, component); reason.terminal {
		return false, reason, nil
	}
	if !deploy.CreationTimestamp.IsZero() && time.Since(deploy.CreationTimestamp.Time) > workloadBootTimeout {
		return false, workloadWaitReason{
			terminal: true,
			message:  fmt.Sprintf("%s did not become ready within %s", component, workloadBootTimeout),
		}, nil
	}
	return false, workloadWaitReason{}, nil
}

func deploymentProgressTerminalReason(deploy *appsv1.Deployment, component string) workloadWaitReason {
	for _, c := range deploy.Status.Conditions {
		if c.Type == appsv1.DeploymentProgressing && c.Reason == "ProgressDeadlineExceeded" && c.Status == corev1.ConditionTrue {
			msg := c.Message
			if msg == "" {
				msg = fmt.Sprintf("%s deployment progress deadline exceeded", component)
			}
			return workloadWaitReason{terminal: true, message: msg}
		}
	}
	return workloadWaitReason{}
}

func (r *ShadowTestReconciler) podsTerminalReason(
	ctx context.Context,
	shadowNS string,
	selector *metav1.LabelSelector,
	component string,
) workloadWaitReason {
	if selector == nil {
		return workloadWaitReason{}
	}
	sel, err := metav1.LabelSelectorAsSelector(selector)
	if err != nil {
		return workloadWaitReason{}
	}
	var pods corev1.PodList
	if err := r.List(ctx, &pods, client.InNamespace(shadowNS), client.MatchingLabelsSelector{Selector: sel}); err != nil {
		return workloadWaitReason{}
	}
	for i := range pods.Items {
		if reason := podTerminalReason(&pods.Items[i], component); reason.terminal {
			return reason
		}
	}
	return workloadWaitReason{}
}

func podTerminalReason(pod *corev1.Pod, component string) workloadWaitReason {
	if reason := containerStatusesTerminal(pod.Name, component, pod.Status.InitContainerStatuses); reason.terminal {
		return reason
	}
	return containerStatusesTerminal(pod.Name, component, pod.Status.ContainerStatuses)
}

func containerStatusesTerminal(podName, component string, statuses []corev1.ContainerStatus) workloadWaitReason {
	for _, cs := range statuses {
		if cs.State.Waiting != nil {
			if _, ok := terminalPodWaitingReasons[cs.State.Waiting.Reason]; ok {
				return workloadWaitReason{
					terminal: true,
					message: fmt.Sprintf("%s pod %s: %s (%s)",
						component, podName, cs.State.Waiting.Reason, cs.State.Waiting.Message),
				}
			}
		}
		if cs.State.Terminated != nil && cs.State.Terminated.ExitCode != 0 {
			return workloadWaitReason{
				terminal: true,
				message: fmt.Sprintf("%s pod %s container %s exited %d: %s",
					component, podName, cs.Name, cs.State.Terminated.ExitCode, cs.State.Terminated.Message),
			}
		}
	}
	return workloadWaitReason{}
}
