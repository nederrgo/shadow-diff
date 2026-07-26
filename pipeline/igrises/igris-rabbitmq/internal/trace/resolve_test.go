package trace

import (
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

func TestResolveContext_generatesWhenNoTraceparent(t *testing.T) {
	t.Parallel()
	got, err := ResolveContext(amqp.Table{})
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

func TestTryInbound_requiresValidTraceparent(t *testing.T) {
	t.Parallel()
	if _, ok := TryInbound(amqp.Table{}); ok {
		t.Fatal("empty headers should not admit")
	}
	if _, ok := TryInbound(amqp.Table{HeaderTraceparent: "not-a-traceparent"}); ok {
		t.Fatal("invalid traceparent should not admit")
	}
	inbound := "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	got, ok := TryInbound(amqp.Table{HeaderTraceparent: inbound})
	if !ok {
		t.Fatal("valid inbound should admit")
	}
	if got.TraceID != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Fatalf("trace id = %q", got.TraceID)
	}
}
