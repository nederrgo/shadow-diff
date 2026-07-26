package trace

import (
	"errors"
	"net/http"
	"strings"
)

// ErrMissingTraceparent is returned when inbound tracing is absent or invalid.
// Tracing is a prerequisite for HTTP multicast — igris does not mint IDs.
var ErrMissingTraceparent = errors.New("missing or invalid traceparent")

// ResolvedContext is computed once before any multicast fan-out.
type ResolvedContext struct {
	TraceID     string
	Traceparent string
}

// ResolveContext reads inbound HTTP headers and returns the trace context to stamp on all clones.
// Inbound traceparent is preserved literally. Missing or invalid values are rejected.
func ResolveContext(headers http.Header) (ResolvedContext, error) {
	inboundTP := strings.TrimSpace(headers.Get(HeaderTraceparent))
	if inboundTP == "" {
		return ResolvedContext{}, ErrMissingTraceparent
	}
	tid, ok := ParseTraceparent(inboundTP)
	if !ok {
		return ResolvedContext{}, ErrMissingTraceparent
	}
	return ResolvedContext{TraceID: tid, Traceparent: inboundTP}, nil
}
