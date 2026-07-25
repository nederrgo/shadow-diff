package controller

import (
	"context"
	"net"
	"sort"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	enginev1alpha1 "github.com/shadow-diff/monarch/api/v1alpha1"
	"github.com/shadow-diff/kaisel/internal/capture"
	"github.com/shadow-diff/kaisel/internal/export"
)

func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	if err := enginev1alpha1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	return s
}

func sortIPs(ips []net.IP) []string {
	strs := make([]string, len(ips))
	for i, ip := range ips {
		strs[i] = ip.String()
	}
	sort.Strings(strs)
	return strs
}

func sortPorts(ports []uint16) []uint16 {
	out := append([]uint16(nil), ports...)
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// reconcileRule is a helper that reconciles a single KaiselRule and returns
// the MapUpdate sent on the updates channel (if any).
func reconcileRule(t *testing.T, r *Reconciler, ch <-chan capture.MapUpdate, name, ns string) (capture.MapUpdate, bool) {
	t.Helper()
	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: name, Namespace: ns},
	})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	select {
	case upd := <-ch:
		return upd, true
	default:
		return capture.MapUpdate{}, false
	}
}

func TestReconciler_RuleCreated(t *testing.T) {
	s := testScheme(t)
	rule := &enginev1alpha1.KaiselRule{
		ObjectMeta: metav1.ObjectMeta{Name: "r1", Namespace: "default"},
		Spec: enginev1alpha1.KaiselRuleSpec{
			TargetIPs:   []string{"10.0.0.1", "10.0.0.2"},
			TargetPorts: []uint16{8080},
		},
	}
	fakeClient := fake.NewClientBuilder().WithScheme(s).WithObjects(rule).Build()
	ch := make(chan capture.MapUpdate, 16)
	router := export.NewRouter(nil)
	r := New(fakeClient, ch, router)

	upd, ok := reconcileRule(t, r, ch, "r1", "default")
	if !ok {
		t.Fatal("expected MapUpdate on create, got none")
	}
	got := sortIPs(upd.AddIPs)
	if len(got) != 2 || got[0] != "10.0.0.1" || got[1] != "10.0.0.2" {
		t.Errorf("AddIPs = %v, want [10.0.0.1 10.0.0.2]", got)
	}
	if len(upd.RemoveIPs) != 0 {
		t.Errorf("RemoveIPs = %v, want []", upd.RemoveIPs)
	}
	if len(upd.AddPorts) != 1 || upd.AddPorts[0] != 8080 {
		t.Errorf("AddPorts = %v, want [8080]", upd.AddPorts)
	}
}

func TestReconciler_RuleUpdated_IPRemoved(t *testing.T) {
	s := testScheme(t)
	rule := &enginev1alpha1.KaiselRule{
		ObjectMeta: metav1.ObjectMeta{Name: "r1", Namespace: "default"},
		Spec: enginev1alpha1.KaiselRuleSpec{
			TargetIPs:   []string{"10.0.0.1", "10.0.0.2"},
			TargetPorts: []uint16{8080},
		},
	}
	fakeClient := fake.NewClientBuilder().WithScheme(s).WithObjects(rule).Build()
	ch := make(chan capture.MapUpdate, 16)
	r := New(fakeClient, ch, export.NewRouter(nil))

	// First reconcile: establish state
	reconcileRule(t, r, ch, "r1", "default")
	// Drain channel
	select {
	case <-ch:
	default:
	}

	// Update: remove 10.0.0.1, add 10.0.0.3
	rule.Spec.TargetIPs = []string{"10.0.0.2", "10.0.0.3"}
	if err := fakeClient.Update(context.Background(), rule); err != nil {
		t.Fatalf("Update: %v", err)
	}

	upd, ok := reconcileRule(t, r, ch, "r1", "default")
	if !ok {
		t.Fatal("expected MapUpdate on update, got none")
	}
	addStrs := sortIPs(upd.AddIPs)
	removeStrs := sortIPs(upd.RemoveIPs)
	if len(addStrs) != 1 || addStrs[0] != "10.0.0.3" {
		t.Errorf("AddIPs = %v, want [10.0.0.3]", addStrs)
	}
	if len(removeStrs) != 1 || removeStrs[0] != "10.0.0.1" {
		t.Errorf("RemoveIPs = %v, want [10.0.0.1]", removeStrs)
	}
	if len(upd.AddPorts) != 0 || len(upd.RemovePorts) != 0 {
		t.Errorf("unexpected port changes: add=%v remove=%v", upd.AddPorts, upd.RemovePorts)
	}
}

func TestReconciler_RuleDeleted(t *testing.T) {
	s := testScheme(t)
	rule := &enginev1alpha1.KaiselRule{
		ObjectMeta: metav1.ObjectMeta{Name: "r1", Namespace: "default"},
		Spec: enginev1alpha1.KaiselRuleSpec{
			TargetIPs:   []string{"10.0.0.1"},
			TargetPorts: []uint16{8080},
		},
	}
	fakeClient := fake.NewClientBuilder().WithScheme(s).WithObjects(rule).Build()
	ch := make(chan capture.MapUpdate, 16)
	r := New(fakeClient, ch, export.NewRouter(nil))

	reconcileRule(t, r, ch, "r1", "default") // result discarded; drains one item from ch

	// Delete the rule
	if err := fakeClient.Delete(context.Background(), rule); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	upd, ok := reconcileRule(t, r, ch, "r1", "default")
	if !ok {
		t.Fatal("expected MapUpdate on delete, got none")
	}
	if len(upd.AddIPs) != 0 {
		t.Errorf("AddIPs = %v after delete, want []", upd.AddIPs)
	}
	removeStrs := sortIPs(upd.RemoveIPs)
	if len(removeStrs) != 1 || removeStrs[0] != "10.0.0.1" {
		t.Errorf("RemoveIPs = %v, want [10.0.0.1]", removeStrs)
	}
	removePorts := sortPorts(upd.RemovePorts)
	if len(removePorts) != 1 || removePorts[0] != 8080 {
		t.Errorf("RemovePorts = %v, want [8080]", removePorts)
	}
}

func TestReconciler_NoOpOnNoChange(t *testing.T) {
	s := testScheme(t)
	rule := &enginev1alpha1.KaiselRule{
		ObjectMeta: metav1.ObjectMeta{Name: "r1", Namespace: "default"},
		Spec: enginev1alpha1.KaiselRuleSpec{
			TargetIPs:   []string{"10.0.0.1"},
			TargetPorts: []uint16{8080},
		},
	}
	fakeClient := fake.NewClientBuilder().WithScheme(s).WithObjects(rule).Build()
	ch := make(chan capture.MapUpdate, 16)
	r := New(fakeClient, ch, export.NewRouter(nil))

	reconcileRule(t, r, ch, "r1", "default") // result discarded; drains one item from ch

	// Reconcile again with identical spec — no update should be sent
	_, ok := reconcileRule(t, r, ch, "r1", "default")
	if ok {
		t.Error("got spurious MapUpdate on re-reconcile with no change")
	}
}

func TestReconciler_ExportRouterPopulated(t *testing.T) {
	s := testScheme(t)
	rule := &enginev1alpha1.KaiselRule{
		ObjectMeta: metav1.ObjectMeta{Name: "r1", Namespace: "default"},
		Spec: enginev1alpha1.KaiselRuleSpec{
			TargetIPs:        []string{"10.0.0.1"},
			TargetPorts:      []uint16{8080},
			IgrisBaseURL:     "http://st-igris.shadow-default-st.svc.cluster.local:8080",
			SamplePercentage: 25,
		},
	}
	fakeClient := fake.NewClientBuilder().WithScheme(s).WithObjects(rule).Build()
	ch := make(chan capture.MapUpdate, 16)
	router := export.NewRouter(nil)
	r := New(fakeClient, ch, router)

	reconcileRule(t, r, ch, "r1", "default")

	got, ok := router.Lookup("10.0.0.1")
	if !ok {
		t.Fatal("expected export route")
	}
	if got.IgrisBaseURL != rule.Spec.IgrisBaseURL || got.SamplePercentage != 25 {
		t.Fatalf("got %+v", got)
	}
}
