package amqptrace

import (
	"strings"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/shadow-diff/trace"
)

// TryInbound returns a resolved context only when a valid inbound traceparent exists.
func TryInbound(headers amqp.Table) (trace.ResolvedContext, bool) {
	inboundTP, _ := extractAMQPString(headers, trace.HeaderTraceparent)
	return trace.TryFromTraceparent(inboundTP)
}

// StampHeaders applies an already-resolved trace context to a copy of headers.
func StampHeaders(headers amqp.Table, resolved trace.ResolvedContext) amqp.Table {
	out := amqp.Table{}
	for k, v := range headers {
		out[k] = v
	}
	for k := range out {
		if strings.ToLower(k) == "traceparent" {
			delete(out, k)
		}
	}
	out[trace.HeaderTraceparent] = resolved.Traceparent
	return out
}

func extractAMQPString(table amqp.Table, key string) (string, bool) {
	keyLower := strings.ToLower(key)
	for k, v := range table {
		if strings.ToLower(k) != keyLower {
			continue
		}
		switch s := v.(type) {
		case string:
			s = strings.TrimSpace(s)
			return s, s != ""
		case []byte:
			str := strings.TrimSpace(string(s))
			return str, str != ""
		default:
			return "", false
		}
	}
	return "", false
}

// TableToStringMap flattens AMQP headers for JSONL capture (best-effort strings).
func TableToStringMap(headers amqp.Table) map[string]string {
	if len(headers) == 0 {
		return nil
	}
	out := make(map[string]string, len(headers))
	for k, v := range headers {
		if s, ok := stringValue(v); ok {
			out[k] = s
		}
	}
	return out
}

// StringMapToTable rebuilds AMQP headers for replay publish.
func StringMapToTable(m map[string]string) amqp.Table {
	if len(m) == 0 {
		return nil
	}
	out := amqp.Table{}
	for k, v := range m {
		out[k] = v
	}
	return out
}

func stringValue(v any) (string, bool) {
	switch s := v.(type) {
	case string:
		return s, true
	case []byte:
		return string(s), true
	default:
		return "", false
	}
}
