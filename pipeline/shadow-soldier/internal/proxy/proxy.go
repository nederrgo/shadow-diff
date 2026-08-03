// Package proxy is the plain-text TCP data path.
//
// One listener per configured dependency accepts on loopback inside the shadow
// pod, forwards bytes verbatim to the real dependency Service, and copies the
// client->server direction into a protocol parser as it goes.
//
// The overriding rule is fail-open: a parser error, a tap overflow, or an
// unrecognised frame must never break, delay, or corrupt the application's
// database connection. Observation is best-effort; the socket is not.
package proxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/shadow-diff/shadow-soldier/internal/parsers"
)

// Route binds a loopback port to one upstream dependency.
type Route struct {
	Protocol string `json:"protocol"`
	Listen   int    `json:"listen"`
	Upstream string `json:"upstream"`
}

// Validate checks a route is usable before any socket is opened.
func (r Route) Validate() error {
	if !parsers.Supported(r.Protocol) {
		return fmt.Errorf("route %q: unsupported protocol", r.Protocol)
	}
	if r.Listen < 1 || r.Listen > 65535 {
		return fmt.Errorf("route %s: listen port %d out of range", r.Protocol, r.Listen)
	}
	if strings.TrimSpace(r.Upstream) == "" {
		return fmt.Errorf("route %s: upstream is required", r.Protocol)
	}
	return nil
}

// Server runs every configured route until its context is cancelled.
type Server struct {
	Log    *slog.Logger
	Routes []Route

	// Emit receives every decoded command. It must not block.
	Emit func(parsers.QueryReport)

	MaxConns    int
	DialTimeout time.Duration
	IdleTimeout time.Duration
	TapChunks   int

	// ListenHost is the address listeners bind to. Loopback in production: the
	// app container reaches the proxy over 127.0.0.1, which the shadow pod's
	// iptables rules explicitly exempt from Envoy's egress redirect.
	ListenHost string

	// OnListenersReady is called once after every route listener is bound.
	// Used to flip the HTTP /healthz readiness probe.
	OnListenersReady func()

	sem   chan struct{}
	conns sync.WaitGroup
}

// Run binds every route and serves until ctx is cancelled. It returns after all
// listeners are closed and in-flight connections have drained.
func (s *Server) Run(ctx context.Context) error {
	s.applyDefaults()

	listeners := make([]net.Listener, 0, len(s.Routes))
	defer func() {
		for _, ln := range listeners {
			_ = ln.Close()
		}
		s.conns.Wait()
	}()

	var wg sync.WaitGroup
	for _, route := range s.Routes {
		if err := route.Validate(); err != nil {
			return err
		}
		addr := net.JoinHostPort(s.ListenHost, fmt.Sprint(route.Listen))
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			return fmt.Errorf("listen %s: %w", addr, err)
		}
		listeners = append(listeners, ln)
		s.Log.Info("shadow-soldier route listening",
			"protocol", route.Protocol, "listen", addr, "upstream", route.Upstream)

		wg.Add(1)
		go func(ln net.Listener, route Route) {
			defer wg.Done()
			s.accept(ctx, ln, route)
		}(ln, route)
	}

	if s.OnListenersReady != nil {
		s.OnListenersReady()
	}

	<-ctx.Done()
	for _, ln := range listeners {
		_ = ln.Close()
	}
	wg.Wait()
	return nil
}

func (s *Server) applyDefaults() {
	if s.Log == nil {
		s.Log = slog.Default()
	}
	if s.Emit == nil {
		s.Emit = func(parsers.QueryReport) {}
	}
	if s.MaxConns <= 0 {
		s.MaxConns = 512
	}
	if s.DialTimeout <= 0 {
		s.DialTimeout = 5 * time.Second
	}
	if s.TapChunks <= 0 {
		s.TapChunks = 256
	}
	if s.ListenHost == "" {
		s.ListenHost = "127.0.0.1"
	}
	s.sem = make(chan struct{}, s.MaxConns)
}

func (s *Server) accept(ctx context.Context, ln net.Listener, route Route) {
	for {
		client, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil || isClosedNetErr(err) {
				return
			}
			s.Log.Warn("accept failed", "protocol", route.Protocol, "err", err)
			continue
		}
		select {
		case s.sem <- struct{}{}:
		default:
			// Refusing beats queueing: a client that cannot connect retries,
			// whereas an unbounded accept loop turns a connection storm into an
			// OOM kill of the whole shadow pod.
			s.Log.Warn("connection limit reached, rejecting",
				"protocol", route.Protocol, "max_conns", s.MaxConns)
			_ = client.Close()
			continue
		}
		s.conns.Add(1)
		go func() {
			defer func() {
				<-s.sem
				s.conns.Done()
			}()
			s.handle(ctx, client, route)
		}()
	}
}

func (s *Server) handle(ctx context.Context, client net.Conn, route Route) {
	defer client.Close()

	dialer := net.Dialer{Timeout: s.DialTimeout}
	upstream, err := dialer.DialContext(ctx, "tcp", route.Upstream)
	if err != nil {
		s.Log.Warn("upstream dial failed",
			"protocol", route.Protocol, "upstream", route.Upstream, "err", err)
		return
	}
	defer upstream.Close()

	var clientRead io.Reader = &idleConn{Conn: client, idle: s.IdleTimeout}
	if route.Protocol == parsers.ProtocolPostgres {
		clientRead, err = negotiatePostgresPlaintext(readWriter{r: clientRead, w: client})
		if err != nil {
			// The client vanished during the handshake; nothing to relay.
			return
		}
	}

	t := newTap(s.TapChunks)
	var parserDone sync.WaitGroup
	parserDone.Add(1)
	go func() {
		defer parserDone.Done()
		s.parse(t, route)
	}()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		// Server -> client is forwarded untouched; only requests are parsed.
		_, _ = pipe(client, &idleConn{Conn: upstream, idle: s.IdleTimeout})
		// Unblock the request side when the upstream hangs up first.
		_ = client.SetReadDeadline(time.Now())
	}()

	_, copyErr := pipe(upstream, io.TeeReader(clientRead, t))
	t.close()
	_ = upstream.Close()
	_ = client.Close()
	wg.Wait()
	parserDone.Wait()

	if copyErr != nil && !isClosedNetErr(copyErr) {
		s.Log.Debug("connection ended", "protocol", route.Protocol, "err", copyErr)
	}
	if t.Overflowed() {
		s.Log.Warn("parser tap overflowed; queries on this connection went unreported",
			"protocol", route.Protocol, "tap_chunks", s.TapChunks)
	}
}

// parse runs the protocol parser over the tapped request stream. It is fully
// isolated from the data path: a panic here is recovered and costs only this
// connection's telemetry, never its bytes.
func (s *Server) parse(t *tap, route Route) {
	defer func() {
		if r := recover(); r != nil {
			s.Log.Error("parser panicked; connection continues unparsed",
				"protocol", route.Protocol, "panic", r)
		}
		// Drain whatever is left so the copy loop's writes are never observed
		// to block, even after the parser has given up.
		_, _ = io.Copy(io.Discard, t)
	}()

	p, err := parsers.New(route.Protocol)
	if err != nil {
		s.Log.Error("no parser for protocol", "protocol", route.Protocol, "err", err)
		return
	}
	if err := p.Run(t, s.Emit); err != nil && !errors.Is(err, io.EOF) {
		s.Log.Debug("parser stopped", "protocol", route.Protocol, "err", err)
	}
}

// readWriter lets the Postgres handshake read through the idle-deadline wrapper
// while writing its one-byte reply straight to the socket.
type readWriter struct {
	r io.Reader
	w io.Writer
}

func (rw readWriter) Read(p []byte) (int, error)  { return rw.r.Read(p) }
func (rw readWriter) Write(p []byte) (int, error) { return rw.w.Write(p) }

func isClosedNetErr(err error) bool {
	if err == nil || errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
		return err != nil
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return strings.Contains(opErr.Error(), "use of closed network connection")
	}
	return false
}
