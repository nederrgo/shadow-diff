package controller

import (
	"context"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	enginev1alpha1 "github.com/shadow-diff/monarch/api/v1alpha1"
)

// deleteShadowRoleWorkloads removes control-a/b/candidate Deployments and Services
// (record mode does not run ABC). Envoy ConfigMaps for those roles are left;
// replay recreate/patches them.
func (r *ShadowTestReconciler) deleteShadowRoleWorkloads(
	ctx context.Context,
	st *enginev1alpha1.ShadowTest,
	shadowNS string,
) error {
	sel := client.MatchingLabels{
		labelManagedBy:     valueManagedBy,
		labelShadowTestUID: string(st.UID),
	}
	var deploys appsv1.DeploymentList
	if err := r.List(ctx, &deploys, client.InNamespace(shadowNS), sel); err != nil {
		return err
	}
	for i := range deploys.Items {
		d := &deploys.Items[i]
		role := d.Labels[labelRole]
		if role != roleControlA && role != roleControlB && role != roleCandidate {
			continue
		}
		if err := r.deleteOwned(ctx, d); err != nil {
			return err
		}
	}

	var svcs corev1.ServiceList
	if err := r.List(ctx, &svcs, client.InNamespace(shadowNS), sel); err != nil {
		return err
	}
	for i := range svcs.Items {
		s := &svcs.Items[i]
		role := s.Labels[labelRole]
		if role != roleControlA && role != roleControlB && role != roleCandidate {
			continue
		}
		if err := r.deleteOwned(ctx, s); err != nil {
			return err
		}
	}
	return nil
}
