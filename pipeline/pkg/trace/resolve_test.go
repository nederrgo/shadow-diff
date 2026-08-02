package trace

import (
	"errors"
	"net/http"
	"testing"
)

func TestResolveHTTP_preservesInbound(t *testing.T) {
	t.Parallel()
	inbound := "01-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-bbbbbbbbbbbbbbbb-01"
	h := http.Header{}
	h.Set(HeaderTraceparent, inbound)
	got, err := ResolveHTTP(h)
	if err != nil {
		t.Fatal(err)
	}
	if got.Traceparent != inbound {
		t.Fatalf("traceparent = %q", got.Traceparent)
	}
	if got.TraceID != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Fatalf("trace id = %q", got.TraceID)
	}
}

func TestResolveHTTP_rejectsNaked(t *testing.T) {
	t.Parallel()
	_, err := ResolveHTTP(http.Header{})
	if !errors.Is(err, ErrMissingTraceparent) {
		t.Fatalf("err = %v", err)
	}
}

func TestTryFromTraceparent(t *testing.T) {
	t.Parallel()
	if _, ok := TryFromTraceparent(""); ok {
		t.Fatal("empty should fail")
	}
	got, ok := TryFromTraceparent("00-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-bbbbbbbbbbbbbbbb-01")
	if !ok || got.TraceID != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Fatalf("got %+v ok=%v", got, ok)
	}
}
