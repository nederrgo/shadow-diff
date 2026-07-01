package trace

import (
	"strings"
	"testing"

	amqp "github.com/rabbitmq/amqp091-go"
)

func TestResolveContext_preservesInboundTraceparentBytes(t *testing.T) {
	t.Parallel()
	inbound := "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	got, err := ResolveContext(amqp.Table{HeaderTraceparent: []byte(inbound)})
	if err != nil {
		t.Fatal(err)
	}
	if got.Traceparent != inbound {
		t.Fatalf("traceparent = %v", got.Traceparent)
	}
	if got.TraceID != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Fatalf("trace id = %q", got.TraceID)
	}
}

func TestResolveContext_ignoresNonHexShadowID(t *testing.T) {
	t.Parallel()
	got, err := ResolveContext(amqp.Table{HeaderShadowTraceID: "existing-trace"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.TraceID) != 32 {
		t.Fatalf("expected generated id, got %q", got.TraceID)
	}
	if !strings.HasPrefix(got.Traceparent, "00-"+got.TraceID) {
		t.Fatalf("traceparent %q", got.Traceparent)
	}
}

func TestResolveContext_fromValidShadowID(t *testing.T) {
	t.Parallel()
	id := strings.Repeat("b", 32)
	got, err := ResolveContext(amqp.Table{HeaderShadowTraceID: id})
	if err != nil {
		t.Fatal(err)
	}
	if got.TraceID != id {
		t.Fatalf("trace id = %q", got.TraceID)
	}
}
