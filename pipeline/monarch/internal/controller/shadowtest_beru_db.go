package controller

import (
	"context"
	"fmt"
	"os"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	enginev1alpha1 "github.com/shadow-diff/monarch/api/v1alpha1"
)

const (
	// envBeruDBSecret names the Secret holding Beru's DB_* connection settings,
	// as "namespace/name" or a bare "name" in defaultBeruDBSecretNS. Set on the
	// manager Deployment, same as BERU_IMAGE. Required — Beru boots only with Postgres.
	envBeruDBSecret = "BERU_DB_SECRET"

	defaultBeruDBSecretNS = "monarch-system"
)

// beruDBSecretRef resolves BERU_DB_SECRET. Empty or malformed values error;
// every beru-local mounts the named Secret via envFrom.
func beruDBSecretRef() (namespace, name string, err error) {
	raw := strings.TrimSpace(os.Getenv(envBeruDBSecret))
	if raw == "" {
		return "", "", fmt.Errorf("BERU_DB_SECRET is required")
	}
	ns, n, found := strings.Cut(raw, "/")
	if !found {
		return defaultBeruDBSecretNS, ns, nil
	}
	ns, n = strings.TrimSpace(ns), strings.TrimSpace(n)
	if ns == "" || n == "" {
		return "", "", fmt.Errorf("BERU_DB_SECRET must be namespace/name or a bare name")
	}
	return ns, n, nil
}

// syncBeruDBSecret copies the Beru database Secret into the shadow namespace so
// beru-local can reference it locally through envFrom.
//
// Mirrors syncStorageSecret: no ownerReferences — the copy is collected when
// reconcileDelete removes the namespace.
func (r *ShadowTestReconciler) syncBeruDBSecret(
	ctx context.Context,
	st *enginev1alpha1.ShadowTest,
	shadowNS string,
) error {
	srcNS, name, err := beruDBSecretRef()
	if err != nil {
		return err
	}

	var src corev1.Secret
	if err := r.Get(ctx, client.ObjectKey{Namespace: srcNS, Name: name}, &src); err != nil {
		return fmt.Errorf("beru database secret %s/%s: %w", srcNS, name, err)
	}

	dst := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: shadowNS,
			Name:      name,
		},
	}
	_, err = ctrl.CreateOrPatch(ctx, r.Client, dst, func() error {
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
