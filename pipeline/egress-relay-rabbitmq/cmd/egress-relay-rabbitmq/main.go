package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/shadow-diff/egress-relay-rabbitmq/internal/beru"
	"github.com/shadow-diff/egress-relay-rabbitmq/internal/config"
	"github.com/shadow-diff/egress-relay-rabbitmq/internal/consumer"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	beruClient := beru.NewClient(cfg.BeruEgressDiffURL())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
		<-sig
		log.Println("egress-relay-rabbitmq shutting down")
		cancel()
	}()

	log.Println("egress-relay-rabbitmq starting")
	consumer.StartAll(ctx, cfg, beruClient)
	<-ctx.Done()
}
