package trace

import (
	"regexp"
	"testing"

	amqp "github.com/rabbitmq/amqp091-go"
)

var traceparentRE = regexp.MustCompile(`^00-[a-f0-9]{32}-[a-f0-9]{16}-01$`)

func TestEnsureTraceHeadersFromTraceparentOnly(t *testing.T) {
	t.Parallel()
	tp := "01-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-bbbbbbbbbbbbbbbb-01"
	got, err := EnsureTraceHeaders(amqp.Table{HeaderTraceparent: tp})
	if err != nil {
		t.Fatal(err)
	}
	if got[HeaderTraceparent] != tp {
		t.Fatalf("traceparent overwritten: %v", got[HeaderTraceparent])
	}
}

func TestEnsureTraceHeadersGeneratesTraceparent(t *testing.T) {
	t.Parallel()
	got, err := EnsureTraceHeaders(nil)
	if err != nil {
		t.Fatal(err)
	}
	tp, ok := got[HeaderTraceparent].(string)
	if !ok || !traceparentRE.MatchString(tp) {
		t.Fatalf("traceparent = %v", got[HeaderTraceparent])
	}
}

func TestEnsureTraceHeadersPreservesInboundTraceparent(t *testing.T) {
	t.Parallel()
	inbound := "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	got, err := EnsureTraceHeaders(amqp.Table{HeaderTraceparent: inbound})
	if err != nil {
		t.Fatal(err)
	}
	if got[HeaderTraceparent] != inbound {
		t.Fatalf("got %v", got[HeaderTraceparent])
	}
}

func TestEnsureTraceIDDelegatesToHeaders(t *testing.T) {
	t.Parallel()
	inbound := "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	got := EnsureTraceID(amqp.Table{HeaderTraceparent: inbound})
	if got[HeaderTraceparent] != inbound {
		t.Fatalf("got %v", got[HeaderTraceparent])
	}
}
