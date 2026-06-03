package capture

import (
	"testing"
	"time"
)

// TestFlushOlderLoopExitsOnStop verifies the 1-minute FlushOlderThan goroutine
// is tied to CaptureManager lifecycle (Sprint 4a.2 OOM guard).
func TestFlushOlderLoopExitsOnStop(t *testing.T) {
	cm := &CaptureManager{stopChan: make(chan struct{})}
	cm.wg.Add(1)
	go cm.flushOlderLoop()
	close(cm.stopChan)

	done := make(chan struct{})
	go func() {
		cm.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("flushOlderLoop did not exit after stopChan closed")
	}
}
