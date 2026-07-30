package multicast

import (
	"testing"

	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/shadow-diff/igris-rabbitmq/internal/capture"
	"github.com/shadow-diff/igris-rabbitmq/internal/config"
	"github.com/shadow-diff/trace"
)

type fakeSink struct {
	recs []capture.IngressCapture
}

func (f *fakeSink) Add(v any) error {
	f.recs = append(f.recs, v.(capture.IngressCapture))
	return nil
}

func TestRecordHandleDelivery_dropsWithoutTraceparent(t *testing.T) {
	t.Parallel()
	sink := &fakeSink{}
	r := &RecordRunner{sink: sink, cfg: config.Config{SamplePercentage: 100}}
	r.handleDelivery(amqp.Delivery{Body: []byte(`{}`), Headers: nil})
	if len(sink.recs) != 0 {
		t.Fatalf("got %d captures, want 0", len(sink.recs))
	}
}

func TestRecordHandleDelivery_capturesWhenSampledIn(t *testing.T) {
	t.Parallel()
	inbound := "00-00000000000000000000000000000087-bbbbbbbbbbbbbbbb-01"
	sink := &fakeSink{}
	r := &RecordRunner{
		sink: sink,
		cfg: config.Config{
			SamplePercentage:      10,
			ShadowPublishExchange: "orders",
		},
	}
	r.handleDelivery(amqp.Delivery{
		Body:         []byte(`{"ok":true}`),
		RoutingKey:   "order.created",
		ContentType:  "application/json",
		DeliveryMode: amqp.Persistent,
		Headers:      amqp.Table{trace.HeaderTraceparent: inbound},
	})
	if len(sink.recs) != 1 {
		t.Fatalf("got %d captures, want 1", len(sink.recs))
	}
	rec := sink.recs[0]
	if rec.Traceparent != inbound || rec.RoutingKey != "order.created" || rec.Exchange != "orders" {
		t.Fatalf("%+v", rec)
	}
	if string(rec.Body) != `{"ok":true}` {
		t.Fatalf("body=%q", rec.Body)
	}
}

func TestRecordHandleDelivery_samplesOut(t *testing.T) {
	t.Parallel()
	inbound := "00-000000000000000000000000000000f9-bbbbbbbbbbbbbbbb-01"
	sink := &fakeSink{}
	r := &RecordRunner{sink: sink, cfg: config.Config{SamplePercentage: 10}}
	r.handleDelivery(amqp.Delivery{
		Body:    []byte(`{}`),
		Headers: amqp.Table{trace.HeaderTraceparent: inbound},
	})
	if len(sink.recs) != 0 {
		t.Fatalf("got %d captures, want 0 (sampled out)", len(sink.recs))
	}
}

func TestPublishCapture_roundTrip(t *testing.T) {
	t.Parallel()
	p := &recordingPub{}
	rec := capture.IngressCapture{
		Traceparent:  "00-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-bbbbbbbbbbbbbbbb-01",
		RoutingKey:   "k",
		ContentType:  "application/json",
		DeliveryMode: amqp.Persistent,
		Body:         []byte(`x`),
	}
	if err := p.PublishCapture(rec); err != nil {
		t.Fatal(err)
	}
	if len(p.recs) != 1 {
		t.Fatalf("%d", len(p.recs))
	}
}

type recordingPub struct {
	recs []capture.IngressCapture
}

func (p *recordingPub) PublishCapture(rec capture.IngressCapture) error {
	p.recs = append(p.recs, rec)
	return nil
}
func (p *recordingPub) Close() {}
