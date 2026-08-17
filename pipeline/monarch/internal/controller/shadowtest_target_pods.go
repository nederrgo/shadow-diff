package controller

import (
	"context"
	"fmt"
	"sort"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func replicaSetOwnedByDeployment(rs *appsv1.ReplicaSet, dep *appsv1.Deployment) bool {
	ref := metav1.GetControllerOf(rs)
	if ref == nil {
		return false
	}
	return ref.APIVersion == "apps/v1" && ref.Kind == "Deployment" && ref.UID == dep.UID
}

func (r *ShadowTestReconciler) replicaSetUIDsForDeployment(
	ctx context.Context,
	ns string,
	dep *appsv1.Deployment,
) (map[types.UID]struct{}, error) {
	var rsList appsv1.ReplicaSetList
	if err := r.List(ctx, &rsList, client.InNamespace(ns)); err != nil {
		return nil, fmt.Errorf("list replicasets: %w", err)
	}
	out := make(map[types.UID]struct{})
	for i := range rsList.Items {
		if replicaSetOwnedByDeployment(&rsList.Items[i], dep) {
			out[rsList.Items[i].UID] = struct{}{}
		}
	}
	return out, nil
}

func (r *ShadowTestReconciler) listPodsOwnedByDeployment(
	ctx context.Context,
	ns string,
	dep *appsv1.Deployment,
) ([]corev1.Pod, error) {
	rsUIDs, err := r.replicaSetUIDsForDeployment(ctx, ns, dep)
	if err != nil {
		return nil, err
	}
	if len(rsUIDs) == 0 {
		return nil, nil
	}

	var podList corev1.PodList
	if err := r.List(ctx, &podList, client.InNamespace(ns)); err != nil {
		return nil, fmt.Errorf("list pods: %w", err)
	}

	var out []corev1.Pod
	for i := range podList.Items {
		pod := &podList.Items[i]
		ref := metav1.GetControllerOf(pod)
		if ref == nil || ref.Kind != "ReplicaSet" {
			continue
		}
		if _, ok := rsUIDs[ref.UID]; ok {
			out = append(out, *pod)
		}
	}
	return out, nil
}

func runningPodIPs(pods []corev1.Pod) []string {
	var ips []string
	for i := range pods {
		pod := &pods[i]
		if pod.DeletionTimestamp != nil {
			continue
		}
		if pod.Status.Phase != corev1.PodRunning {
			continue
		}
		if pod.Status.PodIP == "" {
			continue
		}
		ips = append(ips, pod.Status.PodIP)
	}
	sort.Strings(ips)
	return ips
}

func (r *ShadowTestReconciler) podOwnedByDeployment(
	ctx context.Context,
	pod *corev1.Pod,
	dep *appsv1.Deployment,
) (bool, error) {
	rsRef := metav1.GetControllerOf(pod)
	if rsRef == nil || rsRef.Kind != "ReplicaSet" {
		return false, nil
	}
	var rs appsv1.ReplicaSet
	if err := r.Get(ctx, types.NamespacedName{Namespace: pod.Namespace, Name: rsRef.Name}, &rs); err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	return replicaSetOwnedByDeployment(&rs, dep), nil
}
