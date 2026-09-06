package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/shadow-diff/igris-rabbitmq/internal/capture"
	"github.com/shadow-diff/igris-rabbitmq/internal/config"
	"github.com/shadow-diff/igris-rabbitmq/internal/multicast"
	pkgreplay "github.com/shadow-diff/replay"
	"github.com/shadow-diff/s3utils"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		exitWithError("load config failed", "err", err)
	}
	logger := slog.Default()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
		<-sig
		logger.Info("igris-rabbitmq shutting down")
		cancel()
	}()

	switch cfg.OperatingMode {
	case s3utils.ModeRecord:
		scfg, err := s3utils.ConfigFromEnv(s3utils.DataTypeIngress)
		if err != nil {
			exitWithError("load record S3 config failed", "err", err)
		}
		uploader, err := s3utils.NewBatchUploader(context.Background(), scfg, logger)
		if err != nil {
			exitWithError("create record S3 uploader failed", "err", err)
		}
		defer func() {
			shutdownCtx, c := context.WithTimeout(context.Background(), 30*time.Second)
			defer c()
			_ = uploader.Close(shutdownCtx)
		}()
		runner := multicast.NewRecordRunner(cfg, uploader)
		defer runner.Close()
		logger.Info("igris-rabbitmq record mode", "queue", cfg.ShadowQueueName, "prefix", scfg.ObjectKeyPrefix())
		if err := runner.Run(ctx); err != nil && err != context.Canceled {
			exitWithError("record runner failed", "err", err)
		}

	case s3utils.ModeReplay:
		scfg, err := s3utils.ConfigFromEnv(s3utils.DataTypeIngress)
		if err != nil {
			exitWithError("load replay S3 config failed", "err", err)
		}
		reader, err := s3utils.NewS3Reader(context.Background(), scfg)
		if err != nil {
			exitWithError("create replay S3 reader failed", "err", err)
		}
		loadCtx, loadCancel := context.WithTimeout(context.Background(), 5*time.Minute)
		records, err := pkgreplay.LoadJSONL(loadCtx, reader, logger, func(rec capture.IngressCapture) bool {
			return rec.Traceparent != "" || rec.TraceID != ""
		})
		loadCancel()
		if err != nil {
			exitWithError("replay preload failed", "err", err)
		}
		logger.Info("loaded AMQP ingress records from S3", "records", len(records), "session_id", scfg.SessionID)

		publisher, err := multicast.NewReplayPublisher(cfg)
		if err != nil {
			exitWithError("create shadow publishers failed", "err", err)
		}
		defer publisher.Close()

		engine := &pkgreplay.Engine[capture.IngressCapture]{
			Records:  records,
			Dispatch: publisher.Dispatch,
			Log:      logger,
		}
		mux := http.NewServeMux()
		(&pkgreplay.Handler{Engine: engine}).Mount(mux)
		adminSrv := &http.Server{Addr: cfg.AdminAddr, Handler: mux}
		go func() {
			logger.Info("igris-rabbitmq admin listening", "addr", cfg.AdminAddr)
			if err := adminSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				exitWithError("admin server failed", "err", err)
			}
		}()

		<-ctx.Done()
		shutdownCtx, c := context.WithTimeout(context.Background(), 30*time.Second)
		defer c()
		_ = adminSrv.Shutdown(shutdownCtx)
		engine.Wait()

	default:
		exitWithError("invalid operating mode", "mode", cfg.OperatingMode, "allowed", "record,replay")
	}
}

func exitWithError(message string, args ...any) {
	slog.Error(message, args...)
	os.Exit(1)
}
