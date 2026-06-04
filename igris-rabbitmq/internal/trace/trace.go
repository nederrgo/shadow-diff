package trace

import (
	"fmt"
	"strings"

	amqp "github.com/rabbitmq/amqp091-go"
)

const HeaderShadowTraceID = "x-shadow-trace-id"

// EnsureTraceHeaders ensures outbound AMQP headers carry x-shadow-trace-id and traceparent.
func EnsureTraceHeaders(headers amqp.Table) (amqp.Table, error) {
	out := amqp.Table{}
	for k, v := range headers {
		out[k] = v
	}
	if out == nil {
		out = amqp.Table{}
	}

	inboundTP, hadTP := stringFromTable(out, HeaderTraceparent)
	traceID := strings.TrimSpace(stringFromTableValue(out, HeaderShadowTraceID))
	if traceID == "" && inboundTP != "" {
		if tid, ok := ParseTraceparent(inboundTP); ok {
			traceID = tid
		}
	}
	if traceID == "" {
		var err error
		traceID, err = GenerateTraceID()
		if err != nil {
			return nil, fmt.Errorf("generate trace id: %w", err)
		}
	}

	spanID, err := GenerateSpanID()
	if err != nil {
		return nil, fmt.Errorf("generate span id: %w", err)
	}

	out[HeaderShadowTraceID] = traceID
	if !hadTP {
		out[HeaderTraceparent] = FormatTraceparent(traceID, spanID)
	}
	return out, nil
}

// EnsureTraceID is a compatibility alias for EnsureTraceHeaders.
func EnsureTraceID(headers amqp.Table) amqp.Table {
	out, err := EnsureTraceHeaders(headers)
	if err != nil {
		return headers
	}
	return out
}

func stringFromTable(t amqp.Table, key string) (string, bool) {
	v, ok := t[key]
	if !ok {
		return "", false
	}
	s, ok := stringValue(v)
	return s, ok && s != ""
}

func stringFromTableValue(t amqp.Table, key string) string {
	s, _ := stringFromTable(t, key)
	return s
}

func stringValue(v any) (string, bool) {
	switch s := v.(type) {
	case string:
		return strings.TrimSpace(s), s != ""
	case []byte:
		return strings.TrimSpace(string(s)), len(s) > 0
	default:
		return "", false
	}
}
