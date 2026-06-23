package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/shadow-diff/igris-rabbitmq/internal/config"
	"github.com/shadow-diff/igris-rabbitmq/internal/multicast"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	runner, err := multicast.NewRunner(cfg)
	if err != nil {
		log.Fatalf("runner: %v", err)
	}
	defer runner.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
		<-sig
		log.Println("igris-rabbitmq shutting down")
		cancel()
	}()

	log.Printf("igris-rabbitmq starting queue=%s exchange=%s", cfg.ShadowQueueName, cfg.ShadowPublishExchange)
	if err := runner.Run(ctx); err != nil && err != context.Canceled {
		log.Fatalf("run: %v", err)
	}
}
