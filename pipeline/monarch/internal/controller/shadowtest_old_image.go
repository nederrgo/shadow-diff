package controller

import (
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	enginev1alpha1 "github.com/shadow-diff/monarch/api/v1alpha1"
)

// targetContainerImage returns the primary container image from the target Deployment.
func targetContainerImage(target *appsv1.Deployment) (string, error) {
	if len(target.Spec.Template.Spec.Containers) == 0 {
		return "", fmt.Errorf("target Deployment has no containers")
	}
	return target.Spec.Template.Spec.Containers[0].Image, nil
}

// ensureOldImage pins spec.oldImage on the ShadowTest CR when unset, copying from
// the target Deployment. Returns true when a spec patch was written (caller should requeue).
func (r *ShadowTestReconciler) ensureOldImage(ctx context.Context, st *enginev1alpha1.ShadowTest, target *appsv1.Deployment) (bool, error) {
	if st.Spec.OldImage != "" {
		return false, nil
	}
	image, err := targetContainerImage(target)
	if err != nil {
		return false, err
	}
	base := st.DeepCopy()
	st.Spec.OldImage = image
	if err := r.Patch(ctx, st, client.MergeFrom(base)); err != nil {
		return false, err
	}
	return true, nil
}
