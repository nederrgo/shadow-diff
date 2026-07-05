package trace

import (
	"fmt"
	"net/http"
	"strings"
)

// ResolvedContext is computed once before any multicast fan-out.
type ResolvedContext struct {
	TraceID     string
	Traceparent string
}

// ResolveContext reads inbound HTTP headers and returns the trace context to stamp on all clones.
// Inbound traceparent is preserved literally; otherwise a new W3C pair is generated.
func ResolveContext(headers http.Header) (ResolvedContext, error) {
	inboundTP := strings.TrimSpace(headers.Get(HeaderTraceparent))
	if inboundTP != "" {
		if tid, ok := ParseTraceparent(inboundTP); ok {
			return ResolvedContext{TraceID: tid, Traceparent: inboundTP}, nil
		}
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

func isValidTraceID(s string) bool {
	return len(s) == traceIDLen && isHex(s)
}
