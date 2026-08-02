// Command tusk is the BFF between Monarch's ShadowTest status stream and The
// System UI: topology over WebSockets from gRPC, and ShadowDiff over REST/WS
// from shared Postgres (LISTEN/NOTIFY).
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

	"github.com/shadow-diff/tusk/pkg/db"
	"github.com/shadow-diff/tusk/pkg/server"
)

const shutdownTimeout = 10 * time.Second

func main() {
	log := slog.Default()

	httpAddr := envOr("TUSK_HTTP_ADDR", ":8082")
	monarchAddr := envOr("MONARCH_GRPC_ADDR", "monarch-status-grpc.monarch-system.svc.cluster.local:9090")

	hub := server.NewHub()
	diffHub := server.NewDiffHub()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	var (
		pgStore      *db.Store
		sessionStore server.SessionStore // interface; must stay nil when Postgres is off
	)
	if cfg, ok := db.ConfigFromEnv(); ok {
		opened, err := db.Open(cfg)
		if err != nil {
			log.Error("Postgres open failed; running control-plane-only", "err", err)
		} else {
			pgStore = opened
			sessionStore = opened
			defer func() { _ = pgStore.Close() }()
			log.Info("Postgres connected", "host", cfg.Host, "db", cfg.Name)
			go func() {
				if err := db.Listen(ctx, cfg.DSN(), log, func(ev db.VerdictEvent) {
					if sum, err := pgStore.SessionSummary(ctx, ev.SessionID); err == nil {
						diffHub.BroadcastSummary(sum)
					} else {
						log.Warn("SessionSummary after NOTIFY failed", "err", err, "session_id", ev.SessionID)
					}
					diffHub.BroadcastVerdict(ev)
				}); err != nil && !errors.Is(err, context.Canceled) {
					log.Error("Postgres LISTEN stopped", "err", err)
				}
			}()
		}
	} else {
		log.Warn("DB_HOST/DB_USER/DB_NAME unset; running control-plane-only (no ShadowDiff APIs)")
	}

	httpSrv := &http.Server{
		Addr: httpAddr,
		Handler: (&server.HTTPServer{
			Hub:     hub,
			DiffHub: diffHub,
			Store:   sessionStore,
			Log:     log,
		}).Handler(),
		// No WriteTimeout: WebSocket connections are long-lived and the per-frame
		// deadline in writeGraph / writeDiffFrame bounds writes instead.
		ReadHeaderTimeout: 10 * time.Second,
	}

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
