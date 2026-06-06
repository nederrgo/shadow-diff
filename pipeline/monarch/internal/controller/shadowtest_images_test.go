package controller

import (
	"os"
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
	if got := resolveHelperImage(imageBaseIgrisHTTP, "", envIgrisHTTPImage); got != "igris-http:latest" {
		t.Fatalf("default: got %q want igris-http:latest", got)
	}
}

func TestIgrisHTTPImageFor(t *testing.T) {
	t.Setenv("MONARCH_MODE", "dev")
	t.Setenv(envIgrisHTTPImage, "")

	st := &enginev1alpha1.ShadowTest{}
	if got := igrisHTTPImageFor(st); got != "igris-http:dev" {
		t.Fatalf("mode default: got %q", got)
	}

	st.Spec.Igris = &enginev1alpha1.IgrisSpec{Image: "custom:tag"}
	if got := igrisHTTPImageFor(st); got != "custom:tag" {
		t.Fatalf("CR override: got %q", got)
	}
}

func TestSiphonImageFor(t *testing.T) {
	_ = os.Unsetenv("MONARCH_MODE")
	_ = os.Unsetenv(envSiphonImage)

	st := &enginev1alpha1.ShadowTest{}
	if got := siphonImageFor(st); got != "siphon:latest" {
		t.Fatalf("default: got %q", got)
	}
}
