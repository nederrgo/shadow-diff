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
	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/grpc"

	beruv1 "github.com/shadow-diff/beru/pkg/api/beru/v1"
	"github.com/shadow-diff/beru/internal/api"
	"github.com/shadow-diff/beru/internal/dashboard"
	"github.com/shadow-diff/beru/internal/envoyextproc"
	"github.com/shadow-diff/beru/internal/otlp"
	"github.com/shadow-diff/beru/internal/replay"
	"github.com/shadow-diff/beru/internal/server"
	"github.com/shadow-diff/beru/internal/storage"
	v2engine "github.com/shadow-diff/beru/internal/v2/engine"
	v2storage "github.com/shadow-diff/beru/internal/v2/storage"
)

func main() {
	beruAddr := envOr("BERU_GRPC_ADDR", ":50051")
	otlpAddr := envOr("BERU_OTLP_GRPC_ADDR", ":4317")

	beruLis, err := net.Listen("tcp", beruAddr)
	if err != nil {
		slog.Error("Failed to listen", "addr", beruAddr, "err", err)
		os.Exit(1)
	}
	otlpLis, err := net.Listen("tcp", otlpAddr)
	if err != nil {
		slog.Error("Failed to listen", "addr", otlpAddr, "err", err)
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

	mocks := replay.NewMockStore()
	defaultTest := db.DefaultShadowTestName()

	otlpSrv := &otlp.Server{Log: log, Router: router, DefaultShadowTest: defaultTest}

	dash, err := dashboard.NewHandler(db, v2Repo, log)
	if err != nil {
		slog.Error("Failed to init dashboard", "err", err)
		os.Exit(1)
	}

	httpAddr := envOr("BERU_HTTP_ADDR", ":8080")
	httpSrv := &api.Server{Log: log, Mocks: mocks, Router: router, OTLP: otlpSrv, DB: db, Dashboard: dash}
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
		Log: log, Router: router, Mocks: mocks, Role: envoyextproc.RoleFromEnv(),
		DefaultShadowTest: defaultTest,
	})

	grpcServerOTLP := grpc.NewServer()
	coltracepb.RegisterTraceServiceServer(grpcServerOTLP, otlpSrv)

	go func() {
		log.Info("Beru gRPC server listening", "addr", beruAddr)
		if err := grpcServerBeru.Serve(beruLis); err != nil {
			slog.Error("Beru gRPC server stopped", "err", err)
			os.Exit(1)
		}
	}()
	go func() {
		log.Info("Beru OTLP gRPC server listening", "addr", otlpAddr)
		if err := grpcServerOTLP.Serve(otlpLis); err != nil {
			slog.Error("OTLP gRPC server stopped", "err", err)
			os.Exit(1)
		}
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh
	slog.Info("Shutting down Beru gRPC servers")

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		grpcServerBeru.GracefulStop()
	}()
	go func() {
		defer wg.Done()
		grpcServerOTLP.GracefulStop()
	}()
	wg.Wait()
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
