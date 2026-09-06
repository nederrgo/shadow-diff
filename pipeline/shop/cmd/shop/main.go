package main

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	extprocv3 "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"
	"github.com/shadow-diff/beruclient"
	"github.com/shadow-diff/s3utils"
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
	httpSrv.Ready.Store(true)

	mode, err := s3utils.RequireOperatingMode()
	if err != nil {
		slog.Error("operating mode", "err", err)
		os.Exit(1)
	}
	var uploader *s3utils.BatchUploader

	switch mode {
	case s3utils.ModeRecord:
		cfg, err := s3utils.ConfigFromEnv(s3utils.DataTypeEgress)
		if err != nil {
			slog.Error("record mode S3 config", "err", err)
			os.Exit(1)
		}
		uploader, err = s3utils.NewBatchUploader(context.Background(), cfg, log)
		if err != nil {
			slog.Error("record mode S3 uploader", "err", err)
			os.Exit(1)
		}
		httpSrv.Uploader = uploader
		log.Info("Shop operating mode: record", "bucket", cfg.Bucket, "prefix", cfg.ObjectKeyPrefix())

	case s3utils.ModeReplay:
		httpSrv.Ready.Store(false)
		log.Info("Shop operating mode: replay (preloading egress mocks)")

	default:
		slog.Error("OPERATING_MODE must be record or replay", "got", mode)
		os.Exit(1)
	}

	go func() {
		if err := httpSrv.Start(httpAddr); err != nil && err != http.ErrServerClosed {
			slog.Error("HTTP server stopped", "err", err)
			os.Exit(1)
		}
	}()

	if mode == s3utils.ModeReplay {
		cfg, err := s3utils.ConfigFromEnv(s3utils.DataTypeEgress)
		if err != nil {
			slog.Error("replay mode S3 config", "err", err)
			os.Exit(1)
		}
		reader, err := s3utils.NewS3Reader(context.Background(), cfg)
		if err != nil {
			slog.Error("replay mode S3 reader", "err", err)
			os.Exit(1)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		n, err := replay.PreloadFromS3(ctx, reader, mocks, log)
		cancel()
		if err != nil {
			slog.Error("replay egress preload failed", "err", err)
			os.Exit(1)
		}
		httpSrv.Ready.Store(true)
		log.Info("Shop egress mocks ready", "loaded", n, "prefix", cfg.ObjectKeyPrefix())
	}

	extProc := &envoyextproc.Server{
		Mocks:          mocks,
		ShadowTestName: os.Getenv("SHADOW_TEST_NAME"),
	}
	if beruURL := os.Getenv("BERU_HTTP_URL"); beruURL != "" {
		extProc.Beru = beruclient.NewClient(beruURL)
		log.Info("Shop Beru egress reporting enabled", "url", extProc.Beru.URL)
	}

	grpcSrv := grpc.NewServer()
	extprocv3.RegisterExternalProcessorServer(grpcSrv, extProc)

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
	if uploader != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := uploader.Close(ctx); err != nil {
				slog.Error("S3 uploader close", "err", err)
			}
		}()
	}
	wg.Wait()
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
