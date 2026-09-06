package controller

import (
	"testing"

	enginev1alpha1 "github.com/shadow-diff/monarch/api/v1alpha1"
)

func TestMonarchImageTagSuffix(t *testing.T) {
	t.Setenv("MONARCH_MODE", "dev")
	if got := monarchImageTagSuffix(); got != ":dev" {
		t.Fatalf("dev: got %q want :dev", got)
	}
	t.Setenv("MONARCH_MODE", "development")
	if got := monarchImageTagSuffix(); got != ":dev" {
		t.Fatalf("development: got %q want :dev", got)
	}
	t.Setenv("MONARCH_MODE", "")
	if got := monarchImageTagSuffix(); got != ":latest" {
		t.Fatalf("empty: got %q want :latest", got)
	}
	t.Setenv("MONARCH_MODE", "prod")
	if got := monarchImageTagSuffix(); got != ":latest" {
		t.Fatalf("prod: got %q want :latest", got)
	}
}

func TestResolveHelperImage_precedence(t *testing.T) {
	t.Setenv("MONARCH_MODE", "prod")
	t.Setenv(envIgrisHTTPImage, "")

	if got := resolveHelperImage(imageBaseIgrisHTTP, "cr:override", envIgrisHTTPImage); got != "cr:override" {
		t.Fatalf("CR override: got %q", got)
	}

	t.Setenv(envIgrisHTTPImage, "env:override")
	if got := resolveHelperImage(imageBaseIgrisHTTP, "", envIgrisHTTPImage); got != "env:override" {
		t.Fatalf("env override: got %q", got)
	}

	t.Setenv(envIgrisHTTPImage, "")
	want := imageRegistryDefault + "/igris-http:latest"
	if got := resolveHelperImage(imageBaseIgrisHTTP, "", envIgrisHTTPImage); got != want {
		t.Fatalf("default: got %q want %q", got, want)
	}
}

func TestIgrisHTTPImageFor(t *testing.T) {
	t.Setenv("MONARCH_MODE", "dev")
	t.Setenv(envIgrisHTTPImage, "")

	st := &enginev1alpha1.ShadowTest{}
	want := imageRegistryDefault + "/igris-http:dev"
	if got := igrisHTTPImageFor(st); got != want {
		t.Fatalf("mode default: got %q want %q", got, want)
	}

	st.Spec.Igris = &enginev1alpha1.IgrisSpec{Image: "custom:tag"}
	if got := igrisHTTPImageFor(st); got != "custom:tag" {
		t.Fatalf("CR override: got %q", got)
	}
}

func TestEnvoyImageFor(t *testing.T) {
	t.Setenv(envEnvoyImage, "")
	if got := envoyImageFor(); got != defaultEnvoyImage {
		t.Fatalf("default: got %q want %q", got, defaultEnvoyImage)
	}
	t.Setenv(envEnvoyImage, "custom/envoy:v1")
	if got := envoyImageFor(); got != "custom/envoy:v1" {
		t.Fatalf("env override: got %q", got)
	}
}
