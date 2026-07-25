package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	enginesv1alpha1 "github.com/shadow-diff/monarch/api/v1alpha1"
	"github.com/shadow-diff/pixie-gate/internal/export"
	"github.com/shadow-diff/pixie-gate/internal/pxl"
	"github.com/shadow-diff/pixie-gate/internal/reconcile"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/config"
)

func main() {
	interval := envDuration("PIXIE_EXPORT_INTERVAL_SEC", 3*time.Second)
	stateDir := envOr("PIXIE_GATE_STATE_DIR", "/tmp/pixie-gate")
	tplDir := envOr("PIXIE_PXL_TEMPLATE_DIR", "/etc/pixie-gate")
	healthAddr := envOr("PIXIE_GATE_HEALTH_ADDR", ":8081")

	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		log.Fatalf("state dir: %v", err)
	}

	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(enginesv1alpha1.AddToScheme(scheme))

	cfg, err := config.GetConfig()
	if err != nil {
		log.Fatalf("kubeconfig: %v", err)
	}
	c, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		log.Fatalf("client: %v", err)
	}

	runner := &export.Runner{}
	gate := &reconcile.Gate{
		Client:   c,
		Loader:   pxl.Loader{Dir: tplDir},
		Runner:   runner,
		StateDir: stateDir,
	}

	if err := runner.EnsureAuth(context.Background()); err != nil {
		log.Fatalf("pixie auth: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	go func() {
		log.Printf("health listening on %s", healthAddr)
		if err := http.ListenAndServe(healthAddr, mux); err != nil {
			log.Printf("health server: %v", err)
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	log.Printf("pixie-gate starting (interval=%s state=%s templates=%s)", interval, stateDir, tplDir)
	for {
		if err := gate.ReconcileOnce(ctx); err != nil {
			log.Printf("WARN: reconcile: %v", err)
		}
		select {
		case <-ctx.Done():
			log.Printf("pixie-gate stopping")
			return
		case <-time.After(interval):
		}
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envDuration(key string, def time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	// Accept plain seconds (bash parity) or Go duration strings.
	if secs, err := strconv.Atoi(v); err == nil {
		return time.Duration(secs) * time.Second
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return def
	}
	return d
}
