package http

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"sync"

	"github.com/google/uuid"
	"github.com/shadow-diff/igris/internal/driver"
	"github.com/shadow-diff/igris/internal/payload"
)

const (
	HeaderShadowTraceID = "x-shadow-trace-id"
	driverName          = "http_request"
)

// Driver implements the HTTP request input driver.
type Driver struct {
	Client *http.Client
	Log    *slog.Logger
}

func New() *Driver {
	return &Driver{Client: &http.Client{}}
}

func (d *Driver) Type() driver.Type { return driver.HTTPRequest }

// Session carries per-request state for the HTTP driver.
type Session struct {
	Request *http.Request
	Body    []byte
	Writer  http.ResponseWriter
}

func (s *Session) WriteEarly(early driver.EarlyResponse) error {
	for k, v := range early.Headers {
		s.Writer.Header().Set(k, v)
	}
	s.Writer.WriteHeader(early.StatusCode)
	if len(early.Body) > 0 {
		_, err := s.Writer.Write(early.Body)
		return err
	}
	return nil
}

type listenerEntry struct {
	server *http.Server
	ln     net.Listener
}

// Registry tracks HTTP servers for graceful shutdown.
type Registry struct {
	mu        sync.Mutex
	listeners []*listenerEntry
}

func (r *Registry) track(l *listenerEntry) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.listeners = append(r.listeners, l)
}

func (r *Registry) stop(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	var firstErr error
	for _, l := range r.listeners {
		if err := l.server.Shutdown(ctx); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

var defaultRegistry = &Registry{}

// StopAccepting shuts down all HTTP driver servers.
func StopAccepting(ctx context.Context) error {
	return defaultRegistry.stop(ctx)
}

func (d *Driver) StopAccepting(ctx context.Context) error {
	return defaultRegistry.stop(ctx)
}

func (d *Driver) Listen(ctx context.Context, port int, h driver.Handler) error {
	mux := http.NewServeMux()
	mux.Handle("/", d.handler(h))

	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		return err
	}

	srv := &http.Server{Handler: mux}
	entry := &listenerEntry{server: srv, ln: ln}
	defaultRegistry.track(entry)

	go func() {
		<-ctx.Done()
		_ = srv.Shutdown(context.Background())
	}()

	go func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			slog.Error("HTTP driver server failed", "port", port, "err", err)
		}
	}()

	slog.Info("HTTP driver listening", "port", port)
	return nil
}

func (d *Driver) handler(h driver.Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		_ = r.Body.Close()
		if err != nil {
			http.Error(w, "failed to read request body", http.StatusBadRequest)
			return
		}
		sess := &Session{Request: r, Body: body, Writer: w}
		if err := h.HandleAtomic(d, sess); err != nil {
			if d.Log != nil {
				d.Log.Error("HTTP driver handle failed", "err", err)
			}
			if w.Header().Get("Content-Type") == "" {
				http.Error(w, err.Error(), http.StatusBadRequest)
			}
		}
	}
}

func (d *Driver) ParseMetadata(sess driver.Session) (driver.Metadata, error) {
	s, ok := sess.(*Session)
	if !ok {
		return driver.Metadata{}, fmt.Errorf("invalid HTTP session type")
	}
	traceID := s.Request.Header.Get(HeaderShadowTraceID)
	if traceID == "" {
		traceID = uuid.NewString()
	}
	return driver.Metadata{
		TraceID: traceID,
		Fields: map[string]string{
			"method": s.Request.Method,
			"path":   s.Request.URL.Path,
		},
	}, nil
}

func (d *Driver) Transform(sess driver.Session, meta driver.Metadata) (payload.MulticastMessage, error) {
	s, ok := sess.(*Session)
	if !ok {
		return nil, fmt.Errorf("invalid HTTP session type")
	}
	headers := s.Request.Header.Clone()
	headers.Del("Authorization")
	headers.Del("Cookie")
	headers.Del("Proxy-Authorization")
	headers.Set(HeaderShadowTraceID, meta.TraceID)

	return &message{
		method:     s.Request.Method,
		requestURI: s.Request.URL.RequestURI(),
		headers:    headers,
		body:       s.Body,
		client:     d.Client,
	}, nil
}

func (d *Driver) RespondEarly(meta driver.Metadata) (driver.EarlyResponse, bool) {
	return driver.EarlyResponse{
		StatusCode: http.StatusAccepted,
		Headers: map[string]string{
			HeaderShadowTraceID: meta.TraceID,
		},
	}, true
}

var _ driver.AtomicDriver = (*Driver)(nil)
