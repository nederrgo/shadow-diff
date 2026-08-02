package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/shadow-diff/igris/internal/config"
	"github.com/shadow-diff/igris/internal/core"
	"github.com/shadow-diff/igris/internal/driver"
	httpdriver "github.com/shadow-diff/igris/internal/driver/http"
	"github.com/shadow-diff/igris/internal/payload"
	"github.com/shadow-diff/igris/internal/replay"
	"github.com/shadow-diff/s3utils"
)

func main() {
	cfg := config.Load()
	log := slog.Default()
	hub := core.NewHub(cfg, log)

	var uploader *s3utils.BatchUploader
	var engine *replay.Engine
	var adminSrv *http.Server

	switch cfg.OperatingMode {
	case s3utils.ModeRecord:
		scfg, err := s3utils.ConfigFromEnv(s3utils.DataTypeIngress)
		if err != nil {
			slog.Error("record mode S3 config", "err", err)
			os.Exit(1)
		}
		uploader, err = s3utils.NewBatchUploader(context.Background(), scfg, log)
		if err != nil {
			slog.Error("record mode S3 uploader", "err", err)
			os.Exit(1)
		}
		hub.Uploader = uploader
		log.Info("Igris operating mode: record", "bucket", scfg.Bucket, "prefix", scfg.ObjectKeyPrefix())

	case s3utils.ModeReplay:
		scfg, err := s3utils.ConfigFromEnv(s3utils.DataTypeIngress)
		if err != nil {
			slog.Error("replay mode S3 config", "err", err)
			os.Exit(1)
		}
		reader, err := s3utils.NewS3Reader(context.Background(), scfg)
		if err != nil {
			slog.Error("replay mode S3 reader", "err", err)
			os.Exit(1)
		}
		ctxLoad, cancelLoad := context.WithTimeout(context.Background(), 5*time.Minute)
		records, err := replay.LoadIngress(ctxLoad, reader, log)
		cancelLoad()
		if err != nil {
			slog.Error("replay ingress preload failed", "err", err)
			os.Exit(1)
		}
		log.Info("Loaded ingress records from S3",
			"count", len(records),
			"session", scfg.SessionID,
			"prefix", scfg.ObjectKeyPrefix(),
		)
		targets := make([]payload.Target, 0, 3)
		for _, t := range cfg.Targets() {
			targets = append(targets, payload.Target{Name: t.Name, BaseURL: t.BaseURL})
		}
		engine = replay.NewEngine(records, targets, &http.Client{}, log)
		mux := http.NewServeMux()
		(&replay.Handler{Engine: engine}).Mount(mux)
		adminSrv = &http.Server{Addr: cfg.AdminAddr, Handler: mux}
		go func() {
			log.Info("Igris admin listening", "addr", cfg.AdminAddr)
			if err := adminSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				slog.Error("admin server stopped", "err", err)
				os.Exit(1)
			}
		}()
		log.Info("Igris operating mode: replay")

	default:
		slog.Error("OPERATING_MODE must be record or replay", "got", cfg.OperatingMode)
		os.Exit(1)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	factories := map[string]func() driver.InputDriver{
		"http_request": func() driver.InputDriver { return httpdriver.New(cfg.MaxBodySize, cfg.MaxConcurrency) },
	}

	runDone := make(chan struct{})
	var drivers []driver.InputDriver
	go func() {
		defer close(runDone)
		var err error
		drivers, err = core.Run(ctx, cfg, hub, factories)
		if err != nil {
			slog.Error("Igris hub failed", "err", err)
			os.Exit(1)
		}
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	sig := <-sigCh
	slog.Info("shutdown signal received", "signal", sig.String())

	cancel()
	<-runDone

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutdownCancel()

	if adminSrv != nil {
		_ = adminSrv.Shutdown(shutdownCtx)
	}
	if engine != nil {
		engine.Wait()
	}

	for _, d := range drivers {
		if err := d.StopAccepting(shutdownCtx); err != nil {
			slog.Error("driver shutdown failed", "driver", d.Type(), "err", err)
		}
	}

	slog.Info("waiting for pending atomic multicasts")
	hub.WaitPendingAtomic()
	slog.Info("waiting for pending TCP streams")
	hub.WaitPendingStreams()
	hub.Shutdown()
	if uploader != nil {
		if err := uploader.Close(shutdownCtx); err != nil {
			slog.Error("S3 uploader close", "err", err)
		}
	}
	slog.Info("Igris stopped")
}
