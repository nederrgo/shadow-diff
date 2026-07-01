package driver

import "fmt"

// Metadata is protocol-neutral request metadata for logging and early responses.
type Metadata struct {
	TraceID     string
	Traceparent string
	SpanID      string // deprecated: use Traceparent; kept for callers that read SpanID
	Fields      map[string]string
}

// EarlyResponse is returned to the caller when RespondEarly is true.
type EarlyResponse struct {
	Headers    map[string]string
	StatusCode int
	Body       []byte
}

// Session is opaque to the hub; each driver defines a concrete type.
type Session any

// EarlyWriter is implemented by sessions that support RespondEarly.
type EarlyWriter interface {
	WriteEarly(EarlyResponse) error
}

// WriteEarly writes an early response when the session supports it.
func WriteEarly(sess Session, early EarlyResponse) error {
	if w, ok := sess.(EarlyWriter); ok {
		return w.WriteEarly(early)
	}
	return fmt.Errorf("session does not support early response")
}
