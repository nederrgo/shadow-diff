package main

import (
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	accesslogv3 "github.com/envoyproxy/go-control-plane/envoy/service/accesslog/v3"
	extprocv3 "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"
	"google.golang.org/grpc"

	beruv1 "github.com/shadow-diff/beru/pkg/api/beru/v1"
	"github.com/shadow-diff/beru/internal/als"
	"github.com/shadow-diff/beru/internal/api"
	"github.com/shadow-diff/beru/internal/dashboard"
	"github.com/shadow-diff/beru/internal/egressdiff"
	"github.com/shadow-diff/beru/internal/envoyextproc"
	"github.com/shadow-diff/beru/internal/ingest"
	"github.com/shadow-diff/beru/internal/replay"
	"github.com/shadow-diff/beru/internal/server"
	"github.com/shadow-diff/beru/internal/storage"
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

	db, err := storage.Open(log)
	if err != nil {
		slog.Error("Failed to open storage", "err", err)
		os.Exit(1)
	}
	defer db.Close()

	cfg := ingest.ConfigFromEnv()
	alsStore := als.NewStore(log, cfg)
	alsStore.Storage = db
	store := ingest.NewStore(log, cfg)
	store.Storage = db
	store.OnIngressComplete = alsStore.NotifyIngressComplete
	mocks := replay.NewMockStore()
	egressStore := egressdiff.NewStore(log, egressdiff.ConfigFromEnv())
	egressStore.Storage = db

	dash, err := dashboard.NewHandler(db, log)
	if err != nil {
		slog.Error("Failed to init dashboard", "err", err)
		os.Exit(1)
	}

	httpAddr := os.Getenv("BERU_HTTP_ADDR")
	if httpAddr == "" {
		httpAddr = ":8080"
	}
	httpSrv := &api.Server{Log: log, Mocks: mocks, EgressDiff: egressStore, DB: db, Dashboard: dash}
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
	alsServer := &als.Server{Log: log, Store: alsStore}
	accesslogv3.RegisterAccessLogServiceServer(srv, alsServer)
	log.Info("Beru ALS gRPC registered", "service", "envoy.service.accesslog.v3.AccessLogService")

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
