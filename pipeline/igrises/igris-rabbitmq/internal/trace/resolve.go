package trace

import (
	"fmt"
	"strings"

	amqp "github.com/rabbitmq/amqp091-go"
)

// ResolvedContext is computed once before any multicast fan-out.
type ResolvedContext struct {
	TraceID     string
	Traceparent string
}

// TryInbound returns a resolved context only when a valid inbound traceparent exists.
// Used by the prod sampling gate — tracing is required; missing/invalid → drop.
func TryInbound(headers amqp.Table) (ResolvedContext, bool) {
	inboundTP, _ := extractAMQPString(headers, HeaderTraceparent)
	if inboundTP == "" {
		return ResolvedContext{}, false
	}
	tid, ok := ParseTraceparent(inboundTP)
	if !ok {
		return ResolvedContext{}, false
	}
	return ResolvedContext{TraceID: tid, Traceparent: inboundTP}, true
}

// ResolveContext reads inbound AMQP headers and returns the trace context to stamp on all clones.
// Inbound traceparent is preserved literally; otherwise a new W3C pair is generated.
// Prefer TryInbound at the sampling gate so untraced messages are not admitted.
func ResolveContext(headers amqp.Table) (ResolvedContext, error) {
	if resolved, ok := TryInbound(headers); ok {
		return resolved, nil
	}

	traceID, err := GenerateTraceID()
	if err != nil {
		return ResolvedContext{}, fmt.Errorf("generate trace id: %w", err)
	}
	spanID, err := GenerateSpanID()
	if err != nil {
		return ResolvedContext{}, fmt.Errorf("generate span id: %w", err)
	}
	return ResolvedContext{
		TraceID:     traceID,
		Traceparent: FormatTraceparent(traceID, spanID),
	}, nil
}

func extractAMQPString(table amqp.Table, key string) (string, bool) {
	keyLower := strings.ToLower(key)
	for k, v := range table {
		if strings.ToLower(k) != keyLower {
			continue
		}
		return stringValue(v)
	}
	return "", false
}

func isValidTraceID(s string) bool {
	s = strings.TrimSpace(s)
	return len(s) == traceIDLen && isHex(s)
}

func deleteTraceKeys(table amqp.Table) {
	for k := range table {
		if strings.ToLower(k) == "traceparent" {
			delete(table, k)
		}
	}
}
