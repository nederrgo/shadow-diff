package controller

import (
	"context"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	enginev1alpha1 "github.com/shadow-diff/monarch/api/v1alpha1"
)

// ensureSessionID resolves the S3 session id: mint on record when unset; require
// existing pin on replay. Patches status.currentSessionID when minting or when
// spec.sessionID differs from status.
func (r *ShadowTestReconciler) ensureSessionID(ctx context.Context, st *enginev1alpha1.ShadowTest) (string, error) {
	mode := operatingMode(st)
	specSID := strings.TrimSpace(st.Spec.SessionID)
	statusSID := strings.TrimSpace(st.Status.CurrentSessionID)

	var sid string
	switch mode {
	case modeReplay:
		sid = specSID
		if sid == "" {
			sid = statusSID
		}
		if sid == "" {
			return "", fmt.Errorf("replay mode requires spec.sessionID or status.currentSessionID")
		}
	default: // record
		sid = specSID
		if sid == "" {
			sid = statusSID
		}
		if sid == "" {
			sid = fmt.Sprintf("session-%d", time.Now().Unix())
		}
	}

	if statusSID == sid {
		return sid, nil
	}
	base := st.DeepCopy()
	st.Status.CurrentSessionID = sid
	if err := r.Status().Patch(ctx, st, client.MergeFrom(base)); err != nil {
		return "", err
	}
	return sid, nil
}

// syncStorageSecret copies credentialsSecretRef from the CR namespace into the
// shadow namespace (same name) so pods can use secretKeyRef locally.
func (r *ShadowTestReconciler) syncStorageSecret(
	ctx context.Context,
	st *enginev1alpha1.ShadowTest,
	shadowNS string,
) error {
	cfg := st.Spec.Storage
	if cfg == nil || cfg.CredentialsSecretRef == nil {
		return nil
	}
	name := strings.TrimSpace(cfg.CredentialsSecretRef.Name)
	if name == "" {
		return nil
	}

	var src corev1.Secret
	if err := r.Get(ctx, client.ObjectKey{Namespace: st.Namespace, Name: name}, &src); err != nil {
		return fmt.Errorf("storage credentials secret %s/%s: %w", st.Namespace, name, err)
	}

	dst := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: shadowNS,
			Name:      name,
		},
	}
	_, err := ctrl.CreateOrPatch(ctx, r.Client, dst, func() error {
		if dst.Labels == nil {
			dst.Labels = map[string]string{}
		}
		dst.Labels[labelManagedBy] = valueManagedBy
		dst.Labels[labelShadowTestName] = st.Name
		dst.Labels[labelShadowTestCRNS] = st.Namespace
		dst.Labels[labelShadowTestUID] = string(st.UID)
		dst.Type = src.Type
		dst.Data = src.Data
		return nil
	})
	return err
}

// storageEnvVars builds OPERATING_MODE + S3 env for Igris, igris-rabbitmq, and Shop.
func storageEnvVars(st *enginev1alpha1.ShadowTest, sessionID string) []corev1.EnvVar {
	cfg := st.Spec.Storage
	if cfg == nil {
		return nil
	}
	env := []corev1.EnvVar{
		{Name: envOperatingMode, Value: operatingMode(st)},
		{Name: envS3Bucket, Value: strings.TrimSpace(cfg.BucketName)},
		{Name: envTestNamespace, Value: st.Namespace},
		{Name: envTestName, Value: st.Name},
		{Name: envSessionID, Value: sessionID},
	}
	if ep := strings.TrimSpace(cfg.Endpoint); ep != "" {
		env = append(env, corev1.EnvVar{Name: envS3Endpoint, Value: ep})
	}
	if region := strings.TrimSpace(cfg.Region); region != "" {
		env = append(env, corev1.EnvVar{Name: envS3Region, Value: region})
	}
	if cfg.CredentialsSecretRef != nil {
		sec := strings.TrimSpace(cfg.CredentialsSecretRef.Name)
		if sec != "" {
			env = append(env,
				corev1.EnvVar{
					Name: envAWSAccessKey,
					ValueFrom: &corev1.EnvVarSource{
						SecretKeyRef: &corev1.SecretKeySelector{
							LocalObjectReference: corev1.LocalObjectReference{Name: sec},
							Key:                  secretKeyAccess,
						},
					},
				},
				corev1.EnvVar{
					Name: envAWSSecretKey,
					ValueFrom: &corev1.EnvVarSource{
						SecretKeyRef: &corev1.SecretKeySelector{
							LocalObjectReference: corev1.LocalObjectReference{Name: sec},
							Key:                  secretKeySecret,
						},
					},
				},
			)
		}
	}
	return env
}

// clearReplayState zeros status.replayState when entering record mode.
func (r *ShadowTestReconciler) clearReplayState(ctx context.Context, st *enginev1alpha1.ShadowTest) error {
	if strings.TrimSpace(st.Status.ReplayState) == "" {
		return nil
	}
	base := st.DeepCopy()
	st.Status.ReplayState = ""
	return r.Status().Patch(ctx, st, client.MergeFrom(base))
}

// deleteOwned is a thin Delete that ignores NotFound (for GC helpers).
func (r *ShadowTestReconciler) deleteOwned(ctx context.Context, obj client.Object) error {
	if err := r.Delete(ctx, obj); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	return nil
}
