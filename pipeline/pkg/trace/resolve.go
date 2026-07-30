package trace

import (
	"errors"
	"net/http"
	"strings"
)

// ErrMissingTraceparent is returned when inbound tracing is absent or invalid.
// Tracing is a prerequisite — igris does not mint IDs on the hot path.
var ErrMissingTraceparent = errors.New("missing or invalid traceparent")

// ResolvedContext is computed once before fan-out or S3 capture.
type ResolvedContext struct {
	TraceID     string
	Traceparent string
}

// TryFromTraceparent returns a resolved context only when the value is a valid W3C traceparent.
func TryFromTraceparent(inboundTP string) (ResolvedContext, bool) {
	inboundTP = strings.TrimSpace(inboundTP)
	if inboundTP == "" {
		return ResolvedContext{}, false
	}
	tid, ok := ParseTraceparent(inboundTP)
	if !ok {
		return ResolvedContext{}, false
	}
	return ResolvedContext{TraceID: tid, Traceparent: inboundTP}, true
}

// ResolveHTTP reads inbound HTTP headers and returns the trace context to stamp on clones.
// Inbound traceparent is preserved literally. Missing or invalid values are rejected.
func ResolveHTTP(headers http.Header) (ResolvedContext, error) {
	resolved, ok := TryFromTraceparent(headers.Get(HeaderTraceparent))
	if !ok {
		return ResolvedContext{}, ErrMissingTraceparent
	}
	return resolved, nil
}
