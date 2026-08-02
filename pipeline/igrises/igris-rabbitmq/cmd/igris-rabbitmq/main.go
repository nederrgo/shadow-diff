package main

import (
	"context"
	"log"
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
		log.Fatalf("config: %v", err)
	}
	slogLog := slog.Default()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
		<-sig
		log.Println("igris-rabbitmq shutting down")
		cancel()
	}()

	switch cfg.OperatingMode {
	case s3utils.ModeRecord:
		scfg, err := s3utils.ConfigFromEnv(s3utils.DataTypeIngress)
		if err != nil {
			log.Fatalf("record S3 config: %v", err)
		}
		uploader, err := s3utils.NewBatchUploader(context.Background(), scfg, slogLog)
		if err != nil {
			log.Fatalf("record S3 uploader: %v", err)
		}
		defer func() {
			shutdownCtx, c := context.WithTimeout(context.Background(), 30*time.Second)
			defer c()
			_ = uploader.Close(shutdownCtx)
		}()
		runner := multicast.NewRecordRunner(cfg, uploader)
		defer runner.Close()
		log.Printf("igris-rabbitmq record mode queue=%s prefix=%s", cfg.ShadowQueueName, scfg.ObjectKeyPrefix())
		if err := runner.Run(ctx); err != nil && err != context.Canceled {
			log.Fatalf("run: %v", err)
		}

	case s3utils.ModeReplay:
		scfg, err := s3utils.ConfigFromEnv(s3utils.DataTypeIngress)
		if err != nil {
			log.Fatalf("replay S3 config: %v", err)
		}
		reader, err := s3utils.NewS3Reader(context.Background(), scfg)
		if err != nil {
			log.Fatalf("replay S3 reader: %v", err)
		}
		loadCtx, loadCancel := context.WithTimeout(context.Background(), 5*time.Minute)
		records, err := pkgreplay.LoadJSONL(loadCtx, reader, slogLog, func(rec capture.IngressCapture) bool {
			return rec.Traceparent != "" || rec.TraceID != ""
		})
		loadCancel()
		if err != nil {
			log.Fatalf("replay preload: %v", err)
		}
		log.Printf("loaded %d AMQP ingress records from S3 session=%s", len(records), scfg.SessionID)

		publisher, err := multicast.NewReplayPublisher(cfg)
		if err != nil {
			log.Fatalf("shadow publishers: %v", err)
		}
		defer publisher.Close()

		engine := &pkgreplay.Engine[capture.IngressCapture]{
			Records:  records,
			Dispatch: publisher.Dispatch,
			Log:      slogLog,
		}
		mux := http.NewServeMux()
		(&pkgreplay.Handler{Engine: engine}).Mount(mux)
		adminSrv := &http.Server{Addr: cfg.AdminAddr, Handler: mux}
		go func() {
			log.Printf("igris-rabbitmq admin listening on %s", cfg.AdminAddr)
			if err := adminSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				log.Fatalf("admin: %v", err)
			}
		}()

		<-ctx.Done()
		shutdownCtx, c := context.WithTimeout(context.Background(), 30*time.Second)
		defer c()
		_ = adminSrv.Shutdown(shutdownCtx)
		engine.Wait()

	default:
		log.Fatalf("OPERATING_MODE must be record or replay, got %q", cfg.OperatingMode)
	}
}
