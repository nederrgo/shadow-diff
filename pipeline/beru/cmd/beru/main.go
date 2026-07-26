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

	"github.com/shadow-diff/beru/internal/api"
	"github.com/shadow-diff/beru/internal/dashboard"
	"github.com/shadow-diff/beru/internal/envoyextproc"
	"github.com/shadow-diff/beru/internal/server"
	"github.com/shadow-diff/beru/internal/storage"
	v2engine "github.com/shadow-diff/beru/internal/v2/engine"
	v2storage "github.com/shadow-diff/beru/internal/v2/storage"
	beruv1 "github.com/shadow-diff/beru/pkg/api/beru/v1"
)

func main() {
	beruAddr := envOr("BERU_GRPC_ADDR", ":50051")

	beruLis, err := net.Listen("tcp", beruAddr)
	if err != nil {
		slog.Error("Failed to listen", "addr", beruAddr, "err", err)
		os.Exit(1)
	}

	log := slog.Default()

	db, err := storage.Open(log)
	if err != nil {
		slog.Error("Failed to open storage", "err", err)
		os.Exit(1)
	}
	defer db.Close()

	v2Repo, err := v2storage.NewSQLiteRepository(db.SQL())
	if err != nil {
		slog.Error("Failed to open v2 storage repository", "err", err)
		os.Exit(1)
	}
	router := v2engine.NewTraceRouter(8, v2Repo, db)

	defaultTest := db.DefaultShadowTestName()

	dash, err := dashboard.NewHandler(db, v2Repo, log)
	if err != nil {
		slog.Error("Failed to init dashboard", "err", err)
		os.Exit(1)
	}

	httpAddr := envOr("BERU_HTTP_ADDR", ":8080")
	httpSrv := &api.Server{Log: log, Router: router, DB: db, Dashboard: dash}
	go func() {
		if err := httpSrv.Start(httpAddr); err != nil && err != http.ErrServerClosed {
			slog.Error("HTTP server stopped", "err", err)
			os.Exit(1)
		}
	}()

	grpcServerBeru := grpc.NewServer()
	beruv1.RegisterTrafficReporterServer(grpcServerBeru, &server.TrafficReporter{
		Log: log, Router: router, DefaultShadowTest: defaultTest,
	})
	extprocv3.RegisterExternalProcessorServer(grpcServerBeru, &envoyextproc.Server{
		Log: log, Router: router, Role: envoyextproc.RoleFromEnv(),
		DefaultShadowTest: defaultTest,
	})

	go func() {
		log.Info("Beru gRPC server listening", "addr", beruAddr)
		if err := grpcServerBeru.Serve(beruLis); err != nil {
			slog.Error("Beru gRPC server stopped", "err", err)
			os.Exit(1)
		}
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh
	slog.Info("Shutting down Beru gRPC server")
	grpcServerBeru.GracefulStop()
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
