package main

import (
	"log/slog"
	"net"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	collogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/grpc"
	_ "google.golang.org/grpc/encoding/gzip" // Pixie px.export sends grpc-encoding: gzip

	"github.com/shadow-diff/siphon/internal/forwarder"
	"github.com/shadow-diff/siphon/internal/receiver"
)

func main() {
	log := slog.Default()

	igrisURL := os.Getenv("SIPHON_IGRIS_BASE_URL")
	if igrisURL == "" {
		slog.Error("SIPHON_IGRIS_BASE_URL is required")
		os.Exit(1)
	}

	fwd, err := forwarder.NewClient(igrisURL, 5*time.Second, log)
	if err != nil {
		slog.Error("igris client", "err", err)
		os.Exit(1)
	}

	recv := receiver.NewOTLPReceiver(fwd, envInt("SIPHON_WORKER_COUNT", 8), envInt("SIPHON_JOB_QUEUE_SIZE", 1024), log)
	addr := envOr("SIPHON_OTLP_GRPC_ADDR", ":4317")

	lis, err := net.Listen("tcp", addr)
	if err != nil {
		slog.Error("listen", "addr", addr, "err", err)
		os.Exit(1)
	}

	grpcSrv := grpc.NewServer()
	collogspb.RegisterLogsServiceServer(grpcSrv, receiver.NewLogsService(recv))
	coltracepb.RegisterTraceServiceServer(grpcSrv, receiver.NewTraceService(recv))

	go func() {
		slog.Info("siphon OTLP listening", "addr", addr, "igris", igrisURL)
		if err := grpcSrv.Serve(lis); err != nil {
			slog.Error("grpc serve", "err", err)
			os.Exit(1)
		}
	}()

	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
	<-ch
	slog.Info("shutting down")
	grpcSrv.GracefulStop()
	recv.Stop()
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return def
	}
	return n
}
