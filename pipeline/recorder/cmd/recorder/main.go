package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/shadow-diff/recorder/internal/beru"
	"github.com/shadow-diff/recorder/internal/config"
	"github.com/shadow-diff/recorder/internal/ingest"
)

func main() {
	cfg := config.Load()
	log.Printf("Recorder starting listen=%s downstreams=%d", cfg.ListenAddr, len(cfg.Downstreams))

	client := beru.NewClient(cfg.BeruHTTPURL)
	store := ingest.NewSessionStore(client, cfg.Downstreams, cfg.PairTimeout, cfg.MaxFrameBytes)
	defer store.Stop()

	srv := ingest.NewServer(cfg.ListenAddr, store)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
		<-sig
		log.Println("Recorder shutting down")
		cancel()
	}()

	if err := srv.Listen(ctx); err != nil {
		log.Fatalf("Recorder server failed: %v", err)
	}
}
