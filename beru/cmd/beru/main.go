package main

import (
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"

	extprocv3 "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"
	"google.golang.org/grpc"

	beruv1 "github.com/shadow-diff/beru/pkg/api/beru/v1"
	"github.com/shadow-diff/beru/internal/envoyextproc"
	"github.com/shadow-diff/beru/internal/ingest"
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

	srv := grpc.NewServer()
	beruv1.RegisterTrafficReporterServer(srv, &server.TrafficReporter{Log: log, Store: store})
	extprocv3.RegisterExternalProcessorServer(srv, &envoyextproc.Server{
		Log:   log,
		Store: store,
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
