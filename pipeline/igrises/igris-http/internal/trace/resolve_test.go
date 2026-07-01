package trace

import (
	"net/http"
	"strings"
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

func TestResolveContext_fromValidShadowID(t *testing.T) {
	t.Parallel()
	id := strings.Repeat("a", 32)
	h := http.Header{}
	h.Set(HeaderShadowTraceID, id)
	got, err := ResolveContext(h)
	if err != nil {
		t.Fatal(err)
	}
	if got.TraceID != id {
		t.Fatalf("trace id = %q", got.TraceID)
	}
	if !strings.HasPrefix(got.Traceparent, "00-"+id+"-") {
		t.Fatalf("traceparent = %q", got.Traceparent)
	}
}

func TestResolveContext_ignoresNonHexShadowID(t *testing.T) {
	t.Parallel()
	h := http.Header{}
	h.Set(HeaderShadowTraceID, "trace-abc")
	got, err := ResolveContext(h)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.TraceID) != 32 {
		t.Fatalf("expected generated 32-hex id, got %q", got.TraceID)
	}
	if _, ok := ParseTraceparent(got.Traceparent); !ok {
		t.Fatalf("invalid traceparent %q", got.Traceparent)
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
