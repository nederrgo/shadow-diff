package firehose

import (
	"encoding/json"
	"testing"

	amqp "github.com/rabbitmq/amqp091-go"
)

func TestGetStringHeader(t *testing.T) {
	table := amqp.Table{
		"s": " hello ",
		"b": []byte("world"),
		"n": 42,
	}
	if got, ok := getStringHeader(table, "s"); !ok || got != "hello" {
		t.Fatalf("string header = %q ok=%v", got, ok)
	}
	if got, ok := getStringHeader(table, "b"); !ok || got != "world" {
		t.Fatalf("bytes header = %q ok=%v", got, ok)
	}
	if _, ok := getStringHeader(table, "n"); ok {
		t.Fatal("expected unsupported type to fail")
	}
	if _, ok := getStringHeader(table, "missing"); ok {
		t.Fatal("expected missing key to fail")
	}
}

func TestTraceIDFromFirehose_traceparentString(t *testing.T) {
	headers := amqp.Table{
		"properties": amqp.Table{
			"headers": amqp.Table{
				"traceparent": "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
			},
		},
	}
	id, err := TraceIDFromFirehose(headers)
	if err != nil {
		t.Fatal(err)
	}
	if id != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Fatalf("trace id = %q", id)
	}
}

func TestTraceIDFromFirehose_traceparentBytes(t *testing.T) {
	headers := amqp.Table{
		"properties": amqp.Table{
			"headers": amqp.Table{
				"traceparent": []byte("00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"),
			},
		},
	}
	id, err := TraceIDFromFirehose(headers)
	if err != nil {
		t.Fatal(err)
	}
	if id != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Fatalf("trace id = %q", id)
	}
}

func TestTraceContextFromFirehose_traceparent(t *testing.T) {
	headers := amqp.Table{
		"properties": amqp.Table{
			"headers": amqp.Table{
				"traceparent": "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
			},
		},
	}
	traceID, spanID, err := TraceContextFromFirehose(headers)
	if err != nil {
		t.Fatal(err)
	}
	if traceID != "4bf92f3577b34da6a3ce929d0e0e4736" || spanID != "00f067aa0ba902b7" {
		t.Fatalf("trace=%q span=%q", traceID, spanID)
	}
}

func TestTraceContextFromFirehose_shadowTraceID(t *testing.T) {
	headers := amqp.Table{
		"properties": amqp.Table{
			"headers": amqp.Table{
				"x-shadow-trace-id": "abc123",
			},
		},
	}
	traceID, spanID, err := TraceContextFromFirehose(headers)
	if err != nil {
		t.Fatal(err)
	}
	if traceID != "abc123" || spanID != "" {
		t.Fatalf("trace=%q span=%q", traceID, spanID)
	}
}

func TestTraceIDFromFirehose_shadowTraceID(t *testing.T) {
	headers := amqp.Table{
		"properties": amqp.Table{
			"headers": amqp.Table{
				"x-shadow-trace-id": "abc123",
			},
		},
	}
	id, err := TraceIDFromFirehose(headers)
	if err != nil {
		t.Fatal(err)
	}
	if id != "abc123" {
		t.Fatalf("trace id = %q", id)
	}
}

func TestTraceIDFromFirehose_defaultExchangePublish(t *testing.T) {
	headers := amqp.Table{
		"exchange_name": "",
		"properties": amqp.Table{
			"headers": amqp.Table{
				"traceparent": "00-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-bbbbbbbbbbbbbbbb-01",
			},
		},
	}
	if !IsPublishTrace("publish.amq.default") {
		t.Fatal("expected publish.amq.default to match publish filter")
	}
	id, err := TraceIDFromFirehose(headers)
	if err != nil {
		t.Fatal(err)
	}
	if id != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Fatalf("trace id = %q", id)
	}
	if ExchangeNameFromTrace(headers) != "" {
		t.Fatalf("empty exchange_name should be accepted")
	}
}

func TestExchangeNameFromPublish_routingKeyFallback(t *testing.T) {
	headers := amqp.Table{"exchange_name": ""}
	if got := ExchangeNameFromPublish(headers, "publish.egress-events"); got != "egress-events" {
		t.Fatalf("exchange = %q, want egress-events", got)
	}
	if got := ExchangeNameFromPublish(amqp.Table{}, "publish.orders"); got != "orders" {
		t.Fatalf("exchange = %q, want orders", got)
	}
}

func TestTraceIDFromFirehose_missingHeadersNoPanic(t *testing.T) {
	if _, err := TraceIDFromFirehose(amqp.Table{}); err == nil {
		t.Fatal("expected error for missing headers")
	}
}

func TestPayloadJSON(t *testing.T) {
	raw, err := PayloadJSON([]byte(`{"order":1}`))
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(raw) {
		t.Fatal("expected valid json")
	}
	if _, err := PayloadJSON([]byte("not-json")); err == nil {
		t.Fatal("expected invalid json to fail")
	}
}
