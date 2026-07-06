package trace

import (
	"strings"

	amqp "github.com/rabbitmq/amqp091-go"
)

// EnsureTraceHeaders ensures outbound AMQP headers carry a W3C traceparent.
func EnsureTraceHeaders(headers amqp.Table) (amqp.Table, error) {
	resolved, err := ResolveContext(headers)
	if err != nil {
		return nil, err
	}
	out := amqp.Table{}
	for k, v := range headers {
		out[k] = v
	}
	deleteTraceKeys(out)
	out[HeaderTraceparent] = resolved.Traceparent
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

func stringValue(v any) (string, bool) {
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
