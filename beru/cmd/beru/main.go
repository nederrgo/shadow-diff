package main

import (
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	extprocv3 "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"
	"google.golang.org/grpc"

	beruv1 "github.com/shadow-diff/beru/pkg/api/beru/v1"
	"github.com/shadow-diff/beru/internal/api"
	"github.com/shadow-diff/beru/internal/envoyextproc"
	"github.com/shadow-diff/beru/internal/ingest"
	"github.com/shadow-diff/beru/internal/replay"
	"github.com/shadow-diff/beru/internal/server"
)

func main() {
	addr := os.Getenv("BERU_GRPC_ADDR")
	if addr == "" {
		addr = ":50051"
	}

	lis, err := net.Listen("tcp", addr)
	if err != nil {
		slog.Error("Failed to listen", "addr", addr, "err", err)
		os.Exit(1)
	}

	log := slog.Default()
	store := ingest.NewStore(log, ingest.ConfigFromEnv())
	mocks := replay.NewMockStore()

	httpAddr := os.Getenv("BERU_HTTP_ADDR")
	if httpAddr == "" {
		httpAddr = ":8080"
	}
	httpSrv := &api.Server{Log: log, Mocks: mocks}
	go func() {
		if err := httpSrv.Start(httpAddr); err != nil && err != http.ErrServerClosed {
			slog.Error("HTTP server stopped", "err", err)
			os.Exit(1)
		}
	}()

	srv := grpc.NewServer()
	beruv1.RegisterTrafficReporterServer(srv, &server.TrafficReporter{Log: log, Store: store})
	extprocv3.RegisterExternalProcessorServer(srv, &envoyextproc.Server{
		Log:   log,
		Store: store,
		Mocks: mocks,
		Role:  envoyextproc.RoleFromEnv(),
	})

	go func() {
		slog.Info("Beru gRPC server listening", "addr", addr)
		if err := srv.Serve(lis); err != nil {
			slog.Error("gRPC server stopped", "err", err)
			os.Exit(1)
		}
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh
	slog.Info("Shutting down Beru gRPC server")
	srv.GracefulStop()
}
