package replay

import (
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"

	"github.com/shadow-diff/igris/internal/driver"
	"github.com/shadow-diff/igris/internal/payload"
)

// Engine holds preloaded ingress records and multicasts them on Start.
type Engine struct {
	Records []driver.IngressCapture
	Targets []payload.Target
	Client  *http.Client
	Log     *slog.Logger

	running atomic.Bool
	wg      sync.WaitGroup
}

// Start begins an async replay if not already running.
// already=true means a replay is in progress (caller should return 409).
func (e *Engine) Start() (total int, already bool) {
	total = len(e.Records)
	if !e.running.CompareAndSwap(false, true) {
		return total, true
	}
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		defer e.running.Store(false)
		e.run()
	}()
	return total, false
}

// Wait blocks until any in-flight replay finishes.
func (e *Engine) Wait() {
	e.wg.Wait()
}

// Running reports whether a replay loop is active.
func (e *Engine) Running() bool {
	return e.running.Load()
}

func (e *Engine) run() {
	log := e.Log
	if log == nil {
		log = slog.Default()
	}
	client := e.Client
	if client == nil {
		client = &http.Client{}
	}
	log.Info("replay loop started", "total_records", len(e.Records))
	for i, rec := range e.Records {
		dispatchRecord(client, log, rec, e.Targets)
		if (i+1)%100 == 0 || i+1 == len(e.Records) {
			log.Info("replay progress", "completed", i+1, "total", len(e.Records))
		}
	}
	log.Info("replay loop finished", "total_records", len(e.Records))
}
