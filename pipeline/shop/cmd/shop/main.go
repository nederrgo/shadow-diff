package main

import (
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"

	extprocv3 "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"
	"google.golang.org/grpc"

	"github.com/shadow-diff/shop/internal/api"
	"github.com/shadow-diff/shop/internal/envoyextproc"
	"github.com/shadow-diff/shop/internal/replay"
)

func main() {
	grpcAddr := envOr("SHOP_GRPC_ADDR", ":50051")
	httpAddr := envOr("SHOP_HTTP_ADDR", ":8080")

	grpcLis, err := net.Listen("tcp", grpcAddr)
	if err != nil {
		slog.Error("Failed to listen (gRPC)", "addr", grpcAddr, "err", err)
		os.Exit(1)
	}

	log := slog.Default()
	mocks := replay.NewMockStore()

	httpSrv := &api.Server{Log: log, Mocks: mocks}
	go func() {
		if err := httpSrv.Start(httpAddr); err != nil && err != http.ErrServerClosed {
			slog.Error("HTTP server stopped", "err", err)
			os.Exit(1)
		}
	}()

	grpcSrv := grpc.NewServer()
	extprocv3.RegisterExternalProcessorServer(grpcSrv, &envoyextproc.Server{Mocks: mocks})

	go func() {
		log.Info("Shop gRPC server listening", "addr", grpcAddr)
		if err := grpcSrv.Serve(grpcLis); err != nil {
			slog.Error("Shop gRPC server stopped", "err", err)
			os.Exit(1)
		}
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh
	slog.Info("Shutting down Shop")

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		grpcSrv.GracefulStop()
	}()
	wg.Wait()
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
