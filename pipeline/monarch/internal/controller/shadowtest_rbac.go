package controller

import (
	"context"
	"os"
	"strings"

	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"

	enginev1alpha1 "github.com/shadow-diff/monarch/api/v1alpha1"
)

// Workload mutation in shadow namespaces is delegated to shadow-workload-role via
// a per-namespace RoleBinding reconciled by ensureShadowNamespaceRBAC.

const (
	shadowWorkloadRoleBindingName    = "monarch-shadow-workload"
	defaultShadowWorkloadClusterRole = "shadow-workload-role"
	defaultManagerSANamespace        = "system"
	defaultManagerSAName             = "controller-manager"
)

func shadowWorkloadClusterRoleName() string {
	if v := strings.TrimSpace(os.Getenv("MONARCH_SHADOW_WORKLOAD_CLUSTER_ROLE")); v != "" {
		return v
	}
	return defaultShadowWorkloadClusterRole
}

func managerServiceAccount() (namespace, name string) {
	ns := strings.TrimSpace(os.Getenv("POD_NAMESPACE"))
	if ns == "" {
		ns = defaultManagerSANamespace
	}
	sa := strings.TrimSpace(os.Getenv("POD_SERVICE_ACCOUNT"))
	if sa == "" {
		sa = defaultManagerSAName
	}
	return ns, sa
}

// ensureShadowNamespaceRBAC binds the manager ServiceAccount to shadow-workload-role
// inside the shadow namespace. ClusterRole permissions apply only where the
// RoleBinding exists, so write access to core workloads is limited to shadow-*.
func (r *ShadowTestReconciler) ensureShadowNamespaceRBAC(
	ctx context.Context,
	st *enginev1alpha1.ShadowTest,
	shadowNS string,
) error {
	if err := validateShadowNamespaceName(shadowNS); err != nil {
		return err
	}
	saNS, saName := managerServiceAccount()
	rb := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: shadowNS,
			Name:      shadowWorkloadRoleBindingName,
			Labels: map[string]string{
				labelManagedBy:      valueManagedBy,
				labelShadowTestName: st.Name,
				labelShadowTestCRNS: st.Namespace,
				labelShadowTestUID:  string(st.UID),
			},
		},
	}
	_, err := ctrl.CreateOrPatch(ctx, r.Client, rb, func() error {
		rb.RoleRef = rbacv1.RoleRef{
			APIGroup: rbacv1.GroupName,
			Kind:     "ClusterRole",
			Name:     shadowWorkloadClusterRoleName(),
		}
		rb.Subjects = []rbacv1.Subject{{
			Kind:      rbacv1.ServiceAccountKind,
			Name:      saName,
			Namespace: saNS,
		}}
		return nil
	})
	return err
}
