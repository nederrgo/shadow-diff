package trace

import (
	"regexp"
	"strings"
	"testing"

	amqp "github.com/rabbitmq/amqp091-go"
)

var traceparentRE = regexp.MustCompile(`^00-[a-f0-9]{32}-[a-f0-9]{16}-01$`)

func TestEnsureTraceHeadersPreservesShadowID(t *testing.T) {
	t.Parallel()
	h := amqp.Table{HeaderShadowTraceID: "existing-trace"}
	got, err := EnsureTraceHeaders(h)
	if err != nil {
		t.Fatal(err)
	}
	if got[HeaderShadowTraceID] != "existing-trace" {
		t.Fatalf("shadow id = %v", got[HeaderShadowTraceID])
	}
	tp, ok := got[HeaderTraceparent].(string)
	if !ok || !strings.Contains(tp, "existing-trace") {
		t.Fatalf("traceparent = %v", got[HeaderTraceparent])
	}
}

func TestEnsureTraceHeadersFromTraceparentOnly(t *testing.T) {
	t.Parallel()
	tp := "01-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-bbbbbbbbbbbbbbbb-01"
	got, err := EnsureTraceHeaders(amqp.Table{HeaderTraceparent: tp})
	if err != nil {
		t.Fatal(err)
	}
	if got[HeaderShadowTraceID] != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Fatalf("shadow id = %v", got[HeaderShadowTraceID])
	}
	if got[HeaderTraceparent] != tp {
		t.Fatalf("traceparent overwritten: %v", got[HeaderTraceparent])
	}
}

func TestEnsureTraceHeadersGeneratesBoth(t *testing.T) {
	t.Parallel()
	got, err := EnsureTraceHeaders(nil)
	if err != nil {
		t.Fatal(err)
	}
	id, ok := got[HeaderShadowTraceID].(string)
	if !ok || len(id) != 32 {
		t.Fatalf("trace id = %v", got[HeaderShadowTraceID])
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
	if got[HeaderShadowTraceID] != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Fatalf("shadow id = %v", got[HeaderShadowTraceID])
	}
}

func TestEnsureTraceIDPreservesExisting(t *testing.T) {
	t.Parallel()
	h := amqp.Table{HeaderShadowTraceID: "existing-trace"}
	got := EnsureTraceID(h)
	if got[HeaderShadowTraceID] != "existing-trace" {
		t.Fatalf("got %v", got[HeaderShadowTraceID])
	}
}
