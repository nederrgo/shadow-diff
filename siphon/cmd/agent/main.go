package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/shadow-diff/siphon/internal/agent"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	a := agent.New(log)
	errCh := make(chan error, 1)
	go func() {
		errCh <- a.Run(ctx)
	}()

	select {
	case sig := <-sigCh:
		log.Info("Shutdown signal received", "signal", sig.String())
		cancel()
	case err := <-errCh:
		if err != nil {
			log.Error("Agent exited with error", "err", err)
			os.Exit(1)
		}
		return
	}

	a.Stop()
	<-errCh
	log.Info("Siphon agent stopped")
}
