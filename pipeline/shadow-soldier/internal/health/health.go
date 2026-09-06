// Package health serves GET /healthz for kubelet readiness probes.
//
// DB proxy routes stay on 127.0.0.1; this listener binds 0.0.0.0 so the probe
// can reach the pod IP. Port must stay in sync with Monarch's soldier container
// readinessProbe (pipeline/monarch/.../shadowtest_soldier.go).
package health

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"sync/atomic"
	"time"
)

// Port is the HTTP readiness listen port (0.0.0.0).
const Port = 19191

// Path is the readiness URL path.
const Path = "/healthz"

// Server answers /healthz with 200 once MarkReady has been called, else 503.
type Server struct {
	Log   *slog.Logger
	ready atomic.Bool
}

// MarkReady flips /healthz to 200. Call after all proxy route listeners are bound.
func (s *Server) MarkReady() {
	s.ready.Store(true)
}

// ListenAndServe binds 0.0.0.0:Port until ctx is cancelled.
func (s *Server) ListenAndServe(ctx context.Context) error {
	if s.Log == nil {
		s.Log = slog.Default()
	}
	mux := http.NewServeMux()
	mux.HandleFunc(Path, s.handle)
	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 2 * time.Second,
	}
	ln, err := net.Listen("tcp", net.JoinHostPort("0.0.0.0", strconv.Itoa(Port)))
	if err != nil {
		return err
	}
	s.Log.Info("shadow-soldier health listening", "addr", ln.Addr().String())

	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.Serve(ln)
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
		err := <-errCh
		if err == http.ErrServerClosed {
			return nil
		}
		return err
	case err := <-errCh:
		if err == http.ErrServerClosed {
			return nil
		}
		return err
	}
}

func (s *Server) handle(w http.ResponseWriter, _ *http.Request) {
	if !s.ready.Load() {
		http.Error(w, "not ready", http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok\n"))
}
