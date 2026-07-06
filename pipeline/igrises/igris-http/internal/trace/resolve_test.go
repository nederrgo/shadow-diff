package trace

import (
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

func TestResolveContext_generatesNaked(t *testing.T) {
	t.Parallel()
	got, err := ResolveContext(http.Header{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := ParseTraceparent(got.Traceparent); !ok {
		t.Fatalf("traceparent %q", got.Traceparent)
	}
	if got.TraceID == "" {
		t.Fatal("empty trace id")
	}
}
