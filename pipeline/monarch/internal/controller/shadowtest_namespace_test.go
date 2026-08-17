package controller

import (
	"context"
	"errors"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	enginev1alpha1 "github.com/shadow-diff/monarch/api/v1alpha1"
)

func TestValidateShadowNamespaceName(t *testing.T) {
	t.Parallel()
	if err := validateShadowNamespaceName("shadow-default-demo"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := validateShadowNamespaceName("prod"); err == nil {
		t.Fatal("expected error for prod namespace")
	}
}

func TestShadowNamespaceForCR_noTruncation(t *testing.T) {
	t.Parallel()
	longName := strings.Repeat("a", 50)
	st := &enginev1alpha1.ShadowTest{
		ObjectMeta: metav1.ObjectMeta{
			Name:      longName,
			Namespace: "default",
		},
	}
	got := shadowNamespaceForCR(st)
	want := "shadow-default-" + longName
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	if len(got) <= 63 {
		t.Fatalf("expected projected name longer than 63, got len=%d", len(got))
	}
}

func TestValidateExistingNamespace(t *testing.T) {
	t.Parallel()
	st := &enginev1alpha1.ShadowTest{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "mine",
			Namespace: "default",
			UID:       "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa",
		},
	}

	t.Run("owned", func(t *testing.T) {
		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: "shadow-default-mine",
				Labels: map[string]string{
					labelShadowTestUID: string(st.UID),
				},
			},
		}
		if err := validateExistingNamespace(ns, st); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("terminating", func(t *testing.T) {
		now := metav1.Now()
		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name:              "shadow-default-mine",
				DeletionTimestamp: &now,
				Labels: map[string]string{
					labelShadowTestUID: string(st.UID),
				},
			},
		}
		err := validateExistingNamespace(ns, st)
		if err == nil {
			t.Fatal("expected terminating error")
		}
		if errors.Is(err, ErrNamespaceCollision) {
			t.Fatalf("terminating must not be collision: %v", err)
		}
		if !strings.Contains(err.Error(), "terminating") {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("foreignUID", func(t *testing.T) {
		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: "shadow-default-mine",
				Labels: map[string]string{
					labelShadowTestUID: "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb",
				},
			},
		}
		err := validateExistingNamespace(ns, st)
		if !errors.Is(err, ErrNamespaceCollision) {
			t.Fatalf("want ErrNamespaceCollision, got %v", err)
		}
	})

	t.Run("missingUID", func(t *testing.T) {
		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: "shadow-default-mine"},
		}
		err := validateExistingNamespace(ns, st)
		if !errors.Is(err, ErrNamespaceCollision) {
			t.Fatalf("want ErrNamespaceCollision, got %v", err)
		}
	})
}

func TestEnsureShadowNamespace(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	_ = enginev1alpha1.AddToScheme(scheme)

	st := &enginev1alpha1.ShadowTest{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "ns-own",
			Namespace: "default",
			UID:       "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa",
		},
	}
	name := shadowNamespaceForCR(st)

	t.Run("createWhenMissing", func(t *testing.T) {
		c := fake.NewClientBuilder().WithScheme(scheme).Build()
		rec := &ShadowTestReconciler{Client: c, Scheme: scheme}
		if err := rec.ensureShadowNamespace(context.Background(), st, name); err != nil {
			t.Fatal(err)
		}
		var ns corev1.Namespace
		if err := c.Get(context.Background(), types.NamespacedName{Name: name}, &ns); err != nil {
			t.Fatal(err)
		}
		if ns.Labels[labelShadowTestUID] != string(st.UID) {
			t.Fatalf("labels = %#v", ns.Labels)
		}
	})

	t.Run("ownedExisting", func(t *testing.T) {
		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: name,
				Labels: map[string]string{
					labelShadowTestUID: string(st.UID),
				},
			},
		}
		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(ns).Build()
		rec := &ShadowTestReconciler{Client: c, Scheme: scheme}
		if err := rec.ensureShadowNamespace(context.Background(), st, name); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("foreignExisting", func(t *testing.T) {
		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: name,
				Labels: map[string]string{
					labelShadowTestUID: "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb",
				},
			},
		}
		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(ns).Build()
		rec := &ShadowTestReconciler{Client: c, Scheme: scheme}
		err := rec.ensureShadowNamespace(context.Background(), st, name)
		if !errors.Is(err, ErrNamespaceCollision) {
			t.Fatalf("want ErrNamespaceCollision, got %v", err)
		}
	})
}
