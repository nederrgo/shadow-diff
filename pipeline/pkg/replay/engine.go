package replay

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
)

// Engine holds preloaded records and runs Dispatch for each on Start.
type Engine[T any] struct {
	Records  []T
	Dispatch func(context.Context, T)
	Log      *slog.Logger

	running atomic.Bool
	wg      sync.WaitGroup
}

// Start begins an async replay if not already running.
// already=true means a replay is in progress (caller should return 409).
func (e *Engine[T]) Start() (total int, already bool) {
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
func (e *Engine[T]) Wait() {
	e.wg.Wait()
}

// Running reports whether a replay loop is active.
func (e *Engine[T]) Running() bool {
	return e.running.Load()
}

func (e *Engine[T]) run() {
	log := e.Log
	if log == nil {
		log = slog.Default()
	}
	dispatch := e.Dispatch
	if dispatch == nil {
		log.Error("replay engine missing Dispatch")
		return
	}
	log.Info("replay loop started", "total_records", len(e.Records))
	ctx := context.Background()
	for i, rec := range e.Records {
		dispatch(ctx, rec)
		if (i+1)%100 == 0 || i+1 == len(e.Records) {
			log.Info("replay progress", "completed", i+1, "total", len(e.Records))
		}
	}
	log.Info("replay loop finished", "total_records", len(e.Records))
}
