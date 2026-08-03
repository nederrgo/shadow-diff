package controller

import (
	"context"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	enginev1alpha1 "github.com/shadow-diff/monarch/api/v1alpha1"
)

func TestEnsureSessionID_RemintsOnReplayToRecord(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := enginev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	st := &enginev1alpha1.ShadowTest{
		ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "default"},
		Spec:       enginev1alpha1.ShadowTestSpec{Mode: modeRecord},
		Status: enginev1alpha1.ShadowTestStatus{
			CurrentSessionID: "sess-old",
			ReplayState:      replayStateStarted,
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(st).WithObjects(st).Build()
	r := &ShadowTestReconciler{Client: c}

	sid, err := r.ensureSessionID(context.Background(), st)
	if err != nil {
		t.Fatal(err)
	}
	if sid == "sess-old" || !strings.HasPrefix(sid, "sess-") {
		t.Fatalf("session = %q, want fresh sess-* mint", sid)
	}
	if st.Status.CurrentSessionID != sid {
		t.Fatalf("status = %q, want %q", st.Status.CurrentSessionID, sid)
	}
}

func TestEnsureSessionID_ReusesWhenStableRecord(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := enginev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	st := &enginev1alpha1.ShadowTest{
		ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "default"},
		Spec:       enginev1alpha1.ShadowTestSpec{Mode: modeRecord},
		Status:     enginev1alpha1.ShadowTestStatus{CurrentSessionID: "sess-keep"},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(st).WithObjects(st).Build()
	r := &ShadowTestReconciler{Client: c}

	sid, err := r.ensureSessionID(context.Background(), st)
	if err != nil {
		t.Fatal(err)
	}
	if sid != "sess-keep" {
		t.Fatalf("session = %q, want sess-keep", sid)
	}
}

func TestEnsureSessionID_HonorsPinnedSpec(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := enginev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	st := &enginev1alpha1.ShadowTest{
		ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "default"},
		Spec: enginev1alpha1.ShadowTestSpec{
			Mode:      modeRecord,
			SessionID: "pinned-session",
		},
		Status: enginev1alpha1.ShadowTestStatus{
			CurrentSessionID: "sess-old",
			ReplayState:      replayStateStarted,
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(st).WithObjects(st).Build()
	r := &ShadowTestReconciler{Client: c}

	sid, err := r.ensureSessionID(context.Background(), st)
	if err != nil {
		t.Fatal(err)
	}
	if sid != "pinned-session" {
		t.Fatalf("session = %q, want pinned-session", sid)
	}
}

func TestEnsureReplayExecutionID_MintsOnce(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := enginev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	st := &enginev1alpha1.ShadowTest{
		ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "default"},
		Spec:       enginev1alpha1.ShadowTestSpec{Mode: modeReplay, SessionID: "sess-1"},
		Status:     enginev1alpha1.ShadowTestStatus{CurrentSessionID: "sess-1"},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(st).WithObjects(st).Build()
	r := &ShadowTestReconciler{Client: c}

	id1, err := r.ensureReplayExecutionID(context.Background(), st)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(id1, "exec-") {
		t.Fatalf("execution = %q, want exec-*", id1)
	}
	id2, err := r.ensureReplayExecutionID(context.Background(), st)
	if err != nil {
		t.Fatal(err)
	}
	if id2 != id1 {
		t.Fatalf("second call = %q, want %q", id2, id1)
	}
}

func TestClearReplayState_ClearsExecutionID(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := enginev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	st := &enginev1alpha1.ShadowTest{
		ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "default"},
		Status: enginev1alpha1.ShadowTestStatus{
			ReplayState:              replayStateStarted,
			CurrentReplayExecutionID: "exec-1",
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(st).WithObjects(st).Build()
	r := &ShadowTestReconciler{Client: c}

	if err := r.clearReplayState(context.Background(), st); err != nil {
		t.Fatal(err)
	}
	if st.Status.ReplayState != "" || st.Status.CurrentReplayExecutionID != "" {
		t.Fatalf("status = %+v, want cleared replay fields", st.Status)
	}
}

func TestMintID_Format(t *testing.T) {
	id := mintID("sess")
	parts := strings.Split(id, "-")
	if len(parts) != 3 || parts[0] != "sess" || len(parts[2]) != 4 {
		t.Fatalf("mintID = %q, want sess-<unix>-<4hex>", id)
	}
}
