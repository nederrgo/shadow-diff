// Command shadow-soldier is the L4b database egress capture sidecar.
//
// It runs beside an unmodified application container inside a shadow pod,
// proxies the application's plain-text database connections to the ephemeral
// per-role dependency, and reports every decoded query to Beru for diffing.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"

	"github.com/shadow-diff/beruclient"
	"github.com/shadow-diff/shadow-soldier/internal/beru"
	"github.com/shadow-diff/shadow-soldier/internal/config"
	"github.com/shadow-diff/shadow-soldier/internal/health"
	"github.com/shadow-diff/shadow-soldier/internal/proxy"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	cfg, err := config.Load()
	if err != nil {
		log.Error("config", "err", err)
		os.Exit(1)
	}

	// A soft memory limit makes the GC work harder as the heap approaches the
	// pod's limit instead of letting the kernel OOM-kill the pod — which would
	// take the application container down with it.
	debug.SetMemoryLimit(cfg.MemoryLimit)

	reporter := &beru.Reporter{
		Log:            log,
		Client:         beruclient.NewClient(cfg.BeruURL, cfg.HTTPTimeout),
		Role:           cfg.Role,
		ShadowTestName: cfg.ShadowTestName,
		ShadowPod:      cfg.ShadowPod,
		Workers:        cfg.Workers,
		QueueSize:      cfg.QueueSize,
	}
	reporter.Start()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	hs := &health.Server{Log: log}
	go func() {
		if err := hs.ListenAndServe(ctx); err != nil {
			log.Error("health server stopped", "err", err)
		}
	}()

	srv := &proxy.Server{
		Log:         log,
		Routes:      cfg.Routes,
		Emit:        reporter.Enqueue,
		MaxConns:    cfg.MaxConns,
		DialTimeout: cfg.DialTimeout,
		IdleTimeout: cfg.IdleTimeout,
		TapChunks:   cfg.TapChunks,
		OnListenersReady: func() {
			hs.MarkReady()
		},
	}

	log.Info("shadow-soldier starting",
		"role", cfg.Role, "shadow_test", cfg.ShadowTestName,
		"routes", len(cfg.Routes), "beru", cfg.BeruURL,
		"health", health.Port)

	runErr := srv.Run(ctx)

	// The proxy has stopped accepting and drained its connections, so no further
	// reports can be produced; draining the queue now delivers what a terminating
	// pod already parsed rather than discarding it.
	reporter.Stop()

	if runErr != nil {
		log.Error("proxy stopped", "err", runErr)
		os.Exit(1)
	}
}
