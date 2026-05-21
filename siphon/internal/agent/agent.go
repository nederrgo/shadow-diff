package agent

import (
	"context"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/google/gopacket/tcpassembly"

	"github.com/shadow-diff/siphon/internal/api"
	"github.com/shadow-diff/siphon/internal/capture"
	"github.com/shadow-diff/siphon/internal/config"
	"github.com/shadow-diff/siphon/internal/forward"
	"github.com/shadow-diff/siphon/internal/reassembly"
)

// Agent wires capture, reassembly, forward, and API.
type Agent struct {
	log    *slog.Logger
	store  *config.Store
	engine *capture.Engine
	hub    *reassembly.Hub
	fwd    *forward.Forwarder
	api    *api.Server

	httpCh chan reassembly.ParsedHTTP
	wg     sync.WaitGroup
}

func New(log *slog.Logger) *Agent {
	if log == nil {
		log = slog.Default()
	}
	store := config.NewStore()
	stats := &capture.Stats{}
	httpCh := make(chan reassembly.ParsedHTTP, 256)
	hub := reassembly.NewHub(log, store, httpCh)
	pool := tcpassembly.NewStreamPool(hub)
	assemblerHandler := capture.NewAssemblerHandler(hub, pool)
	engine := capture.NewEngine(log, assemblerHandler, assemblerHandler, stats)
	fwd := forward.New(log, store)

	apiAddr := os.Getenv("SIPHON_API_ADDR")
	if apiAddr == "" {
		apiAddr = ":8080"
	}

	a := &Agent{
		log:    log,
		store:  store,
		engine: engine,
		hub:    hub,
		fwd:    fwd,
		httpCh: httpCh,
	}
	a.api = api.New(log, apiAddr, store, engine, fwd, hub, a.reload)
	return a
}

func (a *Agent) reload(ctx context.Context, payload config.Payload) error {
	a.store.Set(payload)
	return a.engine.Reload(ctx, payload)
}

// Run starts forwarder, flush loop, and API until ctx is cancelled.
func (a *Agent) Run(ctx context.Context) error {
	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		a.fwd.Run(ctx, a.httpCh)
	}()

	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		a.flushLoop(ctx)
	}()

	return a.api.ListenAndServe(ctx)
}

func (a *Agent) flushLoop(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.engine.FlushStale(time.Now().Add(-2 * time.Minute))
		}
	}
}

func (a *Agent) Stop() {
	a.engine.Stop()
	close(a.httpCh)
	a.wg.Wait()
}
