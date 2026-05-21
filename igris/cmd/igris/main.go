package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/shadow-diff/igris/internal/config"
	"github.com/shadow-diff/igris/internal/core"
	"github.com/shadow-diff/igris/internal/driver"
	httpdriver "github.com/shadow-diff/igris/internal/driver/http"
	tcpdriver "github.com/shadow-diff/igris/internal/driver/tcpstream"
)

func main() {
	cfg := config.Load()
	log := slog.Default()
	hub := core.NewHub(cfg, log)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	factories := map[string]func() driver.InputDriver{
		"http_request": func() driver.InputDriver { return httpdriver.New() },
		"tcp_stream":   func() driver.InputDriver { return tcpdriver.New() },
	}

	runDone := make(chan struct{})
	var drivers []driver.InputDriver
	go func() {
		defer close(runDone)
		var err error
		drivers, err = core.Run(ctx, cfg, hub, factories)
		if err != nil {
			slog.Error("Igris hub failed", "err", err)
			os.Exit(1)
		}
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	sig := <-sigCh
	slog.Info("shutdown signal received", "signal", sig.String())

	cancel()
	<-runDone

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutdownCancel()

	for _, d := range drivers {
		if err := d.StopAccepting(shutdownCtx); err != nil {
			slog.Error("driver shutdown failed", "driver", d.Type(), "err", err)
		}
	}

	slog.Info("waiting for pending atomic multicasts")
	hub.WaitPendingAtomic()
	slog.Info("waiting for pending TCP streams")
	hub.WaitPendingStreams()
	hub.Shutdown()
	slog.Info("Igris stopped")
}
