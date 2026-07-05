package multicast

import (
	"testing"

	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/shadow-diff/igris-rabbitmq/internal/trace"
)

type recordingPublisher struct {
	headers []amqp.Table
}

func (p *recordingPublisher) PublishAll(_ amqp.Delivery, headers amqp.Table) error {
	for i := 0; i < 3; i++ {
		copyTable := amqp.Table{}
		for k, v := range headers {
			copyTable[k] = v
		}
		p.headers = append(p.headers, copyTable)
	}
	return nil
}

func (p *recordingPublisher) Close() {}

func assertIdenticalTraceHeaders(t *testing.T, tables []amqp.Table) {
	t.Helper()
	if len(tables) != 3 {
		t.Fatalf("got %d publishes, want 3", len(tables))
	}
	wantTP := tables[0][trace.HeaderTraceparent]
	for i, h := range tables {
		if h[trace.HeaderTraceparent] != wantTP {
			t.Fatalf("publish %d traceparent = %v, want %v", i, h[trace.HeaderTraceparent], wantTP)
		}
	}
}

func TestHandleDelivery_multicastTraceIdentity_naked(t *testing.T) {
	t.Parallel()
	rec := &recordingPublisher{}
	r := &Runner{publisher: rec}
	msg := amqp.Delivery{Body: []byte(`{}`), Headers: nil}
	r.handleDelivery(msg)
	assertIdenticalTraceHeaders(t, rec.headers)
}

func TestHandleDelivery_multicastTraceIdentity_traceparentOnly(t *testing.T) {
	t.Parallel()
	inbound := "01-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-bbbbbbbbbbbbbbbb-01"
	rec := &recordingPublisher{}
	r := &Runner{publisher: rec}
	msg := amqp.Delivery{
		Body:    []byte(`{}`),
		Headers: amqp.Table{trace.HeaderTraceparent: inbound},
	}
	r.handleDelivery(msg)
	assertIdenticalTraceHeaders(t, rec.headers)
	if rec.headers[0][trace.HeaderTraceparent] != inbound {
		t.Fatalf("traceparent = %v", rec.headers[0][trace.HeaderTraceparent])
	}
}

func TestHandleDelivery_multicastTraceIdentity_noTraceparent(t *testing.T) {
	t.Parallel()
	rec := &recordingPublisher{}
	r := &Runner{publisher: rec}
	msg := amqp.Delivery{
		Body:    []byte(`{}`),
		Headers: amqp.Table{},
	}
	r.handleDelivery(msg)
	assertIdenticalTraceHeaders(t, rec.headers)
	tp, ok := rec.headers[0][trace.HeaderTraceparent].(string)
	if !ok || len(tp) == 0 {
		t.Fatalf("expected generated traceparent, got %v", rec.headers[0][trace.HeaderTraceparent])
	}
}
