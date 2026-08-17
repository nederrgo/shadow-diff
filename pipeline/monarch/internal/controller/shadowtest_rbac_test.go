package controller

import (
	"context"
	"testing"

	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	enginev1alpha1 "github.com/shadow-diff/monarch/api/v1alpha1"
)

func TestEnsureShadowNamespaceRBAC(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "monarch-system")
	t.Setenv("POD_SERVICE_ACCOUNT", "monarch-controller-manager")
	t.Setenv("MONARCH_SHADOW_WORKLOAD_CLUSTER_ROLE", "monarch-shadow-workload-role")

	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	_ = enginev1alpha1.AddToScheme(scheme)

	st := &enginev1alpha1.ShadowTest{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "demo",
			Namespace: "default",
			UID:       "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa",
		},
	}
	shadowNS := shadowNamespaceForCR(st)

	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	rec := &ShadowTestReconciler{Client: c, Scheme: scheme}

	if err := rec.ensureShadowNamespaceRBAC(context.Background(), st, shadowNS); err != nil {
		t.Fatal(err)
	}

	var rb rbacv1.RoleBinding
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: shadowNS, Name: shadowWorkloadRoleBindingName}, &rb); err != nil {
		t.Fatal(err)
	}
	if rb.RoleRef.Name != "monarch-shadow-workload-role" {
		t.Fatalf("roleRef.name = %q", rb.RoleRef.Name)
	}
	if len(rb.Subjects) != 1 || rb.Subjects[0].Namespace != "monarch-system" || rb.Subjects[0].Name != "monarch-controller-manager" {
		t.Fatalf("subjects = %#v", rb.Subjects)
	}
}

func TestEnsureShadowNamespaceRBAC_rejectsNonShadowNamespace(t *testing.T) {
	rec := &ShadowTestReconciler{}
	st := &enginev1alpha1.ShadowTest{ObjectMeta: metav1.ObjectMeta{Name: "x", Namespace: "default"}}
	if err := rec.ensureShadowNamespaceRBAC(context.Background(), st, "prod"); err == nil {
		t.Fatal("expected error for non-shadow namespace")
	}
}
