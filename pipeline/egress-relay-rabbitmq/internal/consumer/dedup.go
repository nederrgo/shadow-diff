package consumer

import (
	"context"
	"crypto/sha256"
	"fmt"
	"sync"
	"time"
)

const (
	dedupWindow    = 100 * time.Millisecond
	pruneAge       = 5 * time.Second
	pruneInterval  = 1 * time.Minute
)

type publishDedup struct {
	recentPublishes sync.Map // key string -> time.Time
}

func newPublishDedup() *publishDedup {
	return &publishDedup{}
}

func (d *publishDedup) shouldForward(traceID, spanID string, payload []byte) bool {
	if spanID == "" {
		return true
	}
	sum := sha256.Sum256(payload)
	key := fmt.Sprintf("%s:%s:%x", traceID, spanID, sum[:8])
	now := time.Now()
	if v, loaded := d.recentPublishes.Load(key); loaded {
		if now.Sub(v.(time.Time)) < dedupWindow {
			return false
		}
	}
	d.recentPublishes.Store(key, now)
	return true
}

func (d *publishDedup) startPruner(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(pruneInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				now := time.Now()
				d.recentPublishes.Range(func(key, value any) bool {
					if now.Sub(value.(time.Time)) > pruneAge {
						d.recentPublishes.Delete(key)
					}
					return true
				})
			}
		}
	}()
}
