package multicast

import (
	"testing"

	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/shadow-diff/igris-rabbitmq/internal/config"
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

func TestHandleDelivery_dropsWithoutTraceparent(t *testing.T) {
	t.Parallel()
	rec := &recordingPublisher{}
	r := &Runner{publisher: rec, cfg: config.Config{SamplePercentage: 100}}
	r.handleDelivery(amqp.Delivery{Body: []byte(`{}`), Headers: nil})
	if len(rec.headers) != 0 {
		t.Fatalf("got %d publishes, want 0 (tracing required)", len(rec.headers))
	}
	r.handleDelivery(amqp.Delivery{Body: []byte(`{}`), Headers: amqp.Table{}})
	if len(rec.headers) != 0 {
		t.Fatalf("got %d publishes, want 0", len(rec.headers))
	}
}

func TestHandleDelivery_multicastTraceIdentity_traceparentOnly(t *testing.T) {
	t.Parallel()
	inbound := "01-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-bbbbbbbbbbbbbbbb-01"
	rec := &recordingPublisher{}
	r := &Runner{publisher: rec, cfg: config.Config{SamplePercentage: 100}}
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

func TestHandleDelivery_samplesOutAtTenPercent(t *testing.T) {
	t.Parallel()
	// V=0x1a=26 → drop at 10% under (V*100)<(10*256)
	inbound := "00-1acccccccccccccccccccccccccccccc-bbbbbbbbbbbbbbbb-01"
	rec := &recordingPublisher{}
	r := &Runner{publisher: rec, cfg: config.Config{SamplePercentage: 10}}
	r.handleDelivery(amqp.Delivery{
		Body:    []byte(`{}`),
		Headers: amqp.Table{trace.HeaderTraceparent: inbound},
	})
	if len(rec.headers) != 0 {
		t.Fatalf("got %d publishes, want 0 (sampled out)", len(rec.headers))
	}
}

func TestHandleDelivery_samplesInAtTenPercent(t *testing.T) {
	t.Parallel()
	inbound := "00-00aaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-bbbbbbbbbbbbbbbb-01"
	rec := &recordingPublisher{}
	r := &Runner{publisher: rec, cfg: config.Config{SamplePercentage: 10}}
	r.handleDelivery(amqp.Delivery{
		Body:    []byte(`{}`),
		Headers: amqp.Table{trace.HeaderTraceparent: inbound},
	})
	assertIdenticalTraceHeaders(t, rec.headers)
}
