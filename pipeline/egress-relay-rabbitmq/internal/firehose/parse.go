package firehose

import (
	"encoding/json"
	"fmt"
	"strings"

	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/shadow-diff/egress-relay-rabbitmq/internal/trace"
)

const (
	headerShadowTraceID = "x-shadow-trace-id"
	headerTraceparent   = "traceparent"
)

// TraceExchange is the RabbitMQ Firehose topic exchange.
func TraceExchange() string { return "amq.rabbitmq.trace" }

// PublishBindKey captures outbound publish events only.
func PublishBindKey() string { return "publish.#" }

// IsPublishTrace reports whether a delivery is a Firehose publish event.
func IsPublishTrace(routingKey string) bool {
	return strings.HasPrefix(routingKey, "publish.")
}

// getStringHeader reads a string header from an AMQP table without panicking.
func getStringHeader(table amqp.Table, key string) (string, bool) {
	if table == nil {
		return "", false
	}
	v, ok := table[key]
	if !ok {
		return "", false
	}
	switch s := v.(type) {
	case string:
		out := strings.TrimSpace(s)
		return out, out != ""
	case []byte:
		out := strings.TrimSpace(string(s))
		return out, out != ""
	default:
		return "", false
	}
}

// getTableHeader reads a nested AMQP table header.
func getTableHeader(table amqp.Table, key string) (amqp.Table, bool) {
	if table == nil {
		return nil, false
	}
	v, ok := table[key]
	if !ok {
		return nil, false
	}
	switch t := v.(type) {
	case amqp.Table:
		return t, true
	default:
		return nil, false
	}
}

// OriginalAppHeaders returns application headers embedded in a Firehose trace message.
func OriginalAppHeaders(traceHeaders amqp.Table) (amqp.Table, error) {
	props, ok := getTableHeader(traceHeaders, "properties")
	if !ok {
		return nil, fmt.Errorf("properties header missing or invalid")
	}
	headers, ok := getTableHeader(props, "headers")
	if !ok {
		return nil, fmt.Errorf("properties.headers missing or invalid")
	}
	return headers, nil
}

// TraceContextFromFirehose extracts trace and span ids from Firehose metadata.
func TraceContextFromFirehose(traceHeaders amqp.Table) (traceID, spanID string, err error) {
	appHeaders, err := OriginalAppHeaders(traceHeaders)
	if err != nil {
		return "", "", err
	}
	if id, ok := getStringHeader(appHeaders, headerShadowTraceID); ok {
		return id, "", nil
	}
	if tp, ok := getStringHeader(appHeaders, headerTraceparent); ok {
		if tid, sid, ok := trace.ParseTraceparent(tp); ok {
			return tid, sid, nil
		}
		return "", "", fmt.Errorf("invalid traceparent header")
	}
	return "", "", fmt.Errorf("no trace id in firehose headers")
}

// TraceIDFromFirehose extracts a trace id from Firehose metadata.
func TraceIDFromFirehose(traceHeaders amqp.Table) (string, error) {
	traceID, _, err := TraceContextFromFirehose(traceHeaders)
	return traceID, err
}

// PayloadJSON validates and returns the original message body as JSON.
func PayloadJSON(body []byte) (json.RawMessage, error) {
	if len(body) == 0 {
		return nil, fmt.Errorf("empty body")
	}
	if !json.Valid(body) {
		return nil, fmt.Errorf("body is not valid JSON")
	}
	return json.RawMessage(body), nil
}

// ExchangeNameFromTrace reads exchange_name when present; empty values are valid.
func ExchangeNameFromTrace(traceHeaders amqp.Table) string {
	name, ok := getStringHeader(traceHeaders, "exchange_name")
	if !ok {
		return ""
	}
	return name
}
