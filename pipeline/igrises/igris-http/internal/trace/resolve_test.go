package trace

import (
	"errors"
	"net/http"
	"testing"
)

func TestResolveContext_preservesInboundTraceparent(t *testing.T) {
	t.Parallel()
	inbound := "01-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-bbbbbbbbbbbbbbbb-01"
	h := http.Header{}
	h.Set(HeaderTraceparent, inbound)
	got, err := ResolveContext(h)
	if err != nil {
		t.Fatal(err)
	}
	if got.Traceparent != inbound {
		t.Fatalf("traceparent = %q, want literal %q", got.Traceparent, inbound)
	}
	if got.TraceID != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Fatalf("trace id = %q", got.TraceID)
	}
}

func TestResolveContext_rejectsNaked(t *testing.T) {
	t.Parallel()
	_, err := ResolveContext(http.Header{})
	if !errors.Is(err, ErrMissingTraceparent) {
		t.Fatalf("err = %v, want ErrMissingTraceparent", err)
	}
}

func TestResolveContext_rejectsInvalid(t *testing.T) {
	t.Parallel()
	h := http.Header{}
	h.Set(HeaderTraceparent, "not-a-traceparent")
	_, err := ResolveContext(h)
	if !errors.Is(err, ErrMissingTraceparent) {
		t.Fatalf("err = %v, want ErrMissingTraceparent", err)
	}
}
