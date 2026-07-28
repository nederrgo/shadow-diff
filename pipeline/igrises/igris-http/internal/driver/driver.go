package driver

import (
	"context"
	"net"

	"github.com/shadow-diff/igris/internal/payload"
)

// Type identifies how ingress traffic is handled.
type Type string

const (
	TCPStream    Type = "TCP_STREAM"
	HTTPRequest  Type = "HTTP_REQUEST"
	AsyncMessage Type = "ASYNC_MESSAGE"
)

// Handler is the core multicast surface (implemented by core.Hub).
type Handler interface {
	HandleAtomic(d AtomicDriver, sess Session) error
	RelayTCP(ctx context.Context, src net.Conn, listenPort int)
}

// InputDriver receives traffic on a listener port and forwards it through the Handler.
type InputDriver interface {
	Type() Type
	Listen(ctx context.Context, port int, h Handler) error
	StopAccepting(ctx context.Context) error
}

// AtomicDriver handles discrete request/response units (e.g. HTTP).
type AtomicDriver interface {
	InputDriver
	ParseMetadata(sess Session) (Metadata, error)
	Transform(sess Session, meta Metadata) (payload.MulticastMessage, error)
	RespondEarly(meta Metadata) (EarlyResponse, bool)
}

// IngressCapture is the JSON-serializable shape written to S3 in record mode.
type IngressCapture struct {
	Traceparent string            `json:"traceparent"`
	TraceID     string            `json:"trace_id"`
	Method      string            `json:"method"`
	Path        string            `json:"path"`
	RequestURI  string            `json:"request_uri"`
	Headers     map[string]string `json:"headers"`
	Body        []byte            `json:"body"`
}

// IngressCapturer builds an S3 ingress record from a session (HTTP driver).
type IngressCapturer interface {
	CaptureIngress(sess Session, meta Metadata) (IngressCapture, error)
}
