package core

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/shadow-diff/igris/internal/config"
	"github.com/shadow-diff/igris/internal/driver"
)

// Hub is the protocol-agnostic Igris core.
type Hub struct {
	HTTPTargets    []Target
	TCPHosts       []config.TargetHost
	Pool           *Pool
	Log            *slog.Logger
	DialTimeout    time.Duration
	IdleTimeout    time.Duration
	MaxTCPConns    int

	pendingAtomic   sync.WaitGroup
	pendingStreams  sync.WaitGroup
	streamSem       chan struct{}
}

// NewHub builds a hub from configuration.
func NewHub(cfg config.Config, log *slog.Logger) *Hub {
	if log == nil {
		log = slog.Default()
	}
	targets := make([]Target, len(cfg.Targets()))
	for i, t := range cfg.Targets() {
		targets[i] = Target{Name: t.Name, BaseURL: t.BaseURL}
	}
	h := &Hub{
		HTTPTargets: targets,
		TCPHosts:    cfg.TargetHosts(),
		Pool:        NewPool(cfg.WorkerPoolSize),
		Log:         log,
		DialTimeout: cfg.TCPDialTimeout,
		IdleTimeout: cfg.TCPIdleTimeout,
		MaxTCPConns: cfg.MaxTCPConns,
	}
	h.streamSem = make(chan struct{}, cfg.MaxTCPConns)
	return h
}

// HandleAtomic runs the driver pipeline and submits multicast work to the pool.
func (h *Hub) HandleAtomic(d driver.AtomicDriver, sess driver.Session) error {
	meta, err := d.ParseMetadata(sess)
	if err != nil {
		return err
	}

	msg, err := d.Transform(sess, meta)
	if err != nil {
		return err
	}

	if early, ok := d.RespondEarly(meta); ok {
		if err := driver.WriteEarly(sess, early); err != nil {
			return err
		}
	}

	h.pendingAtomic.Add(1)
	h.Pool.Submit(func() {
		defer h.pendingAtomic.Done()
		results := msg.Dispatch(context.Background(), h.HTTPTargets)
		h.Log.Info("multicast complete",
			"trace_id", meta.TraceID,
			"method", meta.Fields["method"],
			"path", meta.Fields["path"],
			"driver", d.Type(),
			"results", ResultLogAttrs(results),
		)
	})
	return nil
}

// RelayTCP multicasts a TCP connection to three shadow targets on listenPort.
func (h *Hub) RelayTCP(ctx context.Context, src net.Conn, listenPort int) {
	h.pendingStreams.Add(1)
	go func() {
		defer h.pendingStreams.Done()
		h.relayTCP(ctx, src, listenPort)
	}()
}

func (h *Hub) relayTCP(ctx context.Context, src net.Conn, listenPort int) {
	select {
	case h.streamSem <- struct{}{}:
	case <-ctx.Done():
		_ = src.Close()
		return
	default:
		h.Log.Info("TCP stream rejected: connection limit reached", "port", listenPort)
		_ = src.Close()
		return
	}
	defer func() { <-h.streamSem }()

	addrs := make([]string, len(h.TCPHosts))
	for i, th := range h.TCPHosts {
		addrs[i] = fmt.Sprintf("%s:%d", th.Host, listenPort)
	}

	idle := h.IdleTimeout
	src = &idleConn{Conn: src, idle: idle, log: h.Log}
	defer src.Close()

	dsts := make([]net.Conn, 0, 3)
	defer func() {
		for _, c := range dsts {
			if c != nil {
				_ = c.Close()
			}
		}
	}()

	for _, addr := range addrs {
		dialer := net.Dialer{Timeout: h.DialTimeout}
		dst, err := dialer.DialContext(ctx, "tcp", addr)
		if err != nil {
			h.Log.Info("TCP multicast dial failed", "addr", addr, "err", err)
			return
		}
		dsts = append(dsts, &idleConn{Conn: dst, idle: idle, log: h.Log})
	}

	writers := make([]io.Writer, len(dsts))
	for i, c := range dsts {
		writers[i] = c
	}
	_, err := io.Copy(io.MultiWriter(writers...), src)
	if err != nil && !isClosedNetErr(err) {
		h.Log.Info("TCP relay ended", "port", listenPort, "err", err)
	}
}

func isClosedNetErr(err error) bool {
	if err == nil {
		return false
	}
	return err == io.EOF || err.Error() == "use of closed network connection"
}

// WaitPendingAtomic blocks until in-flight HTTP multicasts complete.
func (h *Hub) WaitPendingAtomic() {
	h.pendingAtomic.Wait()
}

// WaitPendingStreams blocks until active TCP relays complete.
func (h *Hub) WaitPendingStreams() {
	h.pendingStreams.Wait()
}

// WaitPending blocks until all atomic and streaming work completes.
func (h *Hub) WaitPending() {
	h.WaitPendingAtomic()
	h.WaitPendingStreams()
}

// Shutdown stops the worker pool after pending work is drained.
func (h *Hub) Shutdown() {
	h.Pool.Stop()
}

// idleConn resets read deadline on each Read to enforce idle timeout.
type idleConn struct {
	net.Conn
	idle time.Duration
	log  *slog.Logger
}

func (c *idleConn) Read(b []byte) (int, error) {
	if err := c.Conn.SetReadDeadline(time.Now().Add(c.idle)); err != nil {
		return 0, err
	}
	n, err := c.Conn.Read(b)
	if ne, ok := err.(net.Error); ok && ne.Timeout() {
		if c.log != nil {
			c.log.Info("TCP idle timeout", "idle", c.idle.String())
		}
		_ = c.Conn.Close()
		return n, err
	}
	return n, err
}
