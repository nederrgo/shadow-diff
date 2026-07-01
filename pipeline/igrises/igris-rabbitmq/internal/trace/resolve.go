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

// ResolveContext reads inbound AMQP headers and returns the trace context to stamp on all clones.
func ResolveContext(headers amqp.Table) (ResolvedContext, error) {
	inboundTP, _ := extractAMQPString(headers, HeaderTraceparent)
	if inboundTP != "" {
		if tid, ok := ParseTraceparent(inboundTP); ok {
			return ResolvedContext{TraceID: tid, Traceparent: inboundTP}, nil
		}
	}

	shadowID, _ := extractAMQPString(headers, HeaderShadowTraceID)
	if isValidTraceID(shadowID) {
		spanID, err := GenerateSpanID()
		if err != nil {
			return ResolvedContext{}, fmt.Errorf("generate span id: %w", err)
		}
		tid := strings.ToLower(shadowID)
		return ResolvedContext{
			TraceID:     tid,
			Traceparent: FormatTraceparent(tid, spanID),
		}, nil
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
		kl := strings.ToLower(k)
		if kl == "traceparent" || kl == "x-shadow-trace-id" {
			delete(table, k)
		}
	}
}
