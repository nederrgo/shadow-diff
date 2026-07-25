package main

import (
	"context"
	"log"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"

	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/grpc"
	_ "google.golang.org/grpc/encoding/gzip" // Pixie px.export sends grpc-encoding: gzip

	"github.com/shadow-diff/recorder/internal/config"
	"github.com/shadow-diff/recorder/internal/ingest"
	otlprecv "github.com/shadow-diff/recorder/internal/receiver"
	"github.com/shadow-diff/recorder/internal/shop"
)

func main() {
	cfg := config.Load()
	log.Printf("Recorder starting tcp=%s otlp=%s", cfg.ListenAddr, cfg.OTLPGRPCAddr)

	client := shop.NewClient(cfg.ShopHTTPURL)
	store := ingest.NewSessionStore(client, cfg.PairTimeout, cfg.MaxFrameBytes)
	defer store.Stop()

	otlpRecv := otlprecv.NewOTLPReceiver(client, 4, 512, cfg.SamplePercentage, slog.Default())
	defer otlpRecv.Stop()

	tcpSrv := ingest.NewServer(cfg.ListenAddr, store)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	grpcSrv := grpc.NewServer()
	coltracepb.RegisterTraceServiceServer(grpcSrv, otlprecv.NewTraceService(otlpRecv))

	otlpLis, err := net.Listen("tcp", cfg.OTLPGRPCAddr)
	if err != nil {
		log.Fatalf("OTLP listen %s: %v", cfg.OTLPGRPCAddr, err)
	}
	go func() {
		slog.Info("recorder OTLP listening", "addr", cfg.OTLPGRPCAddr)
		if err := grpcSrv.Serve(otlpLis); err != nil {
			slog.Error("grpc serve", "err", err)
			os.Exit(1)
		}
	}()

	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
		<-sig
		log.Println("Recorder shutting down")
		cancel()
		grpcSrv.GracefulStop()
	}()

	if err := tcpSrv.Listen(ctx); err != nil {
		log.Fatalf("Recorder TCP server failed: %v", err)
	}
}
