// Command tusk is the BFF between Monarch's ShadowTest status stream and the
// topology UI: it consumes gRPC updates, converts them to React Flow graphs, and
// fans them out to browsers over WebSockets.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/shadow-diff/tusk/pkg/server"
)

const shutdownTimeout = 10 * time.Second

func main() {
	log := slog.Default()

	httpAddr := envOr("TUSK_HTTP_ADDR", ":8082")
	monarchAddr := envOr("MONARCH_GRPC_ADDR", "monarch-status-grpc.monarch-system.svc.cluster.local:9090")

	hub := server.NewHub()
	httpSrv := &http.Server{
		Addr:    httpAddr,
		Handler: (&server.HTTPServer{Hub: hub, Log: log}).Handler(),
		// No WriteTimeout: WebSocket connections are long-lived and the per-frame
		// deadline in writeGraph bounds writes instead.
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		client := &server.MonarchClient{Addr: monarchAddr, Hub: hub, Log: log}
		if err := client.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			log.Error("Monarch client stopped", "err", err)
		}
	}()

	go func() {
		log.Info("Tusk HTTP server listening", "addr", httpAddr)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("HTTP server stopped", "err", err)
			stop()
		}
	}()

	<-ctx.Done()
	log.Info("Shutting down Tusk")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		log.Error("HTTP shutdown", "err", err)
	}
	wg.Wait()
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
