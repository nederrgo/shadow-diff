package tcpstream

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"sync"

	"github.com/shadow-diff/igris/internal/driver"
)

const driverName = "tcp_stream"

// Driver implements raw TCP stream multicast.
type Driver struct {
	mu       sync.Mutex
	listeners []net.Listener
}

func New() *Driver {
	return &Driver{}
}

func (d *Driver) Type() driver.Type { return driver.TCPStream }

func (d *Driver) StopAccepting(ctx context.Context) error {
	d.mu.Lock()
	ln := d.listeners
	d.listeners = nil
	d.mu.Unlock()
	var firstErr error
	for _, l := range ln {
		if err := l.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (d *Driver) Listen(ctx context.Context, port int, h driver.Handler) error {
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		return err
	}
	d.mu.Lock()
	d.listeners = append(d.listeners, ln)
	d.mu.Unlock()

	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()

	go d.acceptLoop(ctx, ln, port, h)
	slog.Info("TCP stream driver listening", "port", port)
	return nil
}

func (d *Driver) acceptLoop(ctx context.Context, ln net.Listener, port int, h driver.Handler) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return
			default:
			}
			if opErr, ok := err.(*net.OpError); ok && opErr.Err.Error() == "use of closed network connection" {
				return
			}
			slog.Error("TCP accept failed", "port", port, "err", err)
			return
		}
		h.RelayTCP(ctx, conn, port)
	}
}

var _ driver.InputDriver = (*Driver)(nil)
