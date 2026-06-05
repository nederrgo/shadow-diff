package controller

import (
	"testing"

	enginev1alpha1 "github.com/shadow-diff/monarch/api/v1alpha1"
)

func TestOtelInjectionEnabled(t *testing.T) {
	t.Parallel()
	if !otelInjectionEnabled(&enginev1alpha1.ShadowTest{}) {
		t.Fatal("expected default on")
	}
	disabled := false
	if otelInjectionEnabled(&enginev1alpha1.ShadowTest{
		Spec: enginev1alpha1.ShadowTestSpec{
			OtelInjection: &enginev1alpha1.OtelInjectionSpec{Enabled: &disabled},
		},
	}) {
		t.Fatal("expected disabled")
	}
}

func TestDetectOtelLanguage(t *testing.T) {
	t.Parallel()
	cases := []struct {
		image string
		spec  string
		want  string
	}{
		{"eclipse-temurin:17", "", "java"},
		{"python:3.12", "", "python"},
		{"node:20", "", "nodejs"},
		{"mcr.microsoft.com/dotnet/aspnet:8.0", "", "dotnet"},
		{"golang:1.22", "", "go"},
		{"shadow-diff/app:latest", "", ""},
		{"python:3.12", "java", "java"},
	}
	for _, tc := range cases {
		if got := detectOtelLanguage(tc.image, tc.spec); got != tc.want {
			t.Fatalf("detectOtelLanguage(%q, %q) = %q, want %q", tc.image, tc.spec, got, tc.want)
		}
	}
}

func TestOtelPodAnnotations(t *testing.T) {
	t.Parallel()
	st := &enginev1alpha1.ShadowTest{}
	st.Name = "st"
	ann := otelPodAnnotations(st, "eclipse-temurin:17")
	if ann[annotationOtelInjectSDK] != "true" {
		t.Fatal("missing inject-sdk")
	}
	if ann[annotationOtelInjectPrefix+"java"] != "true" {
		t.Fatal("missing inject-java")
	}
}

func TestOtelPodAnnotations_disabledNotAppliedInReconcile(t *testing.T) {
	t.Parallel()
	disabled := false
	st := &enginev1alpha1.ShadowTest{
		Spec: enginev1alpha1.ShadowTestSpec{
			OtelInjection: &enginev1alpha1.OtelInjectionSpec{Enabled: &disabled},
		},
	}
	if otelInjectionEnabled(st) {
		t.Fatal("expected false")
	}
}
