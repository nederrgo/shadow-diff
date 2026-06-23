package core

import (
	"context"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shadow-diff/igris/internal/config"
	"github.com/shadow-diff/igris/internal/driver"
	"github.com/shadow-diff/igris/internal/payload"
)

type stubAtomicDriver struct {
	handleDone chan struct{}
}

func (s *stubAtomicDriver) Type() driver.Type { return driver.HTTPRequest }
func (s *stubAtomicDriver) Listen(ctx context.Context, port int, h driver.Handler) error {
	return nil
}
func (s *stubAtomicDriver) StopAccepting(ctx context.Context) error { return nil }
func (s *stubAtomicDriver) ParseMetadata(sess driver.Session) (driver.Metadata, error) {
	return driver.Metadata{TraceID: "t1", Fields: map[string]string{"method": "GET", "path": "/"}}, nil
}
func (s *stubAtomicDriver) Transform(sess driver.Session, meta driver.Metadata) (payload.MulticastMessage, error) {
	return &stubMessage{done: s.handleDone}, nil
}
func (s *stubAtomicDriver) RespondEarly(meta driver.Metadata) (driver.EarlyResponse, bool) {
	return driver.EarlyResponse{StatusCode: 202}, true
}

type stubMessage struct {
	done chan struct{}
}

func (m *stubMessage) Dispatch(ctx context.Context, targets []Target) []Result {
	close(m.done)
	return nil
}

type stubSession struct{}

func (stubSession) WriteEarly(driver.EarlyResponse) error { return nil }

func testHubConfig() config.Config {
	return config.Config{
		ControlAURL:    "http://127.0.0.1:1",
		ControlBURL:    "http://127.0.0.1:2",
		CandidateURL:   "http://127.0.0.1:3",
		ControlAAddr:   "127.0.0.1",
		ControlBAddr:   "127.0.0.1",
		CandidateAddr:  "127.0.0.1",
		WorkerPoolSize: 2,
		MaxTCPConns:    8,
		TCPDialTimeout: time.Second,
		TCPIdleTimeout: time.Minute,
	}
}

func TestHubWaitsPendingAtomic(t *testing.T) {
	t.Parallel()
	done := make(chan struct{})
	hub := NewHub(testHubConfig(), slog.Default())
	d := &stubAtomicDriver{handleDone: done}
	if err := hub.HandleAtomic(d, stubSession{}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("multicast not executed")
	}
	hub.WaitPendingAtomic()
}

func TestShutdownOrderStopBeforeWait(t *testing.T) {
	t.Parallel()
	var acceptStopped atomic.Bool
	hub := NewHub(testHubConfig(), slog.Default())
	acceptStopped.Store(true)
	if !acceptStopped.Load() {
		t.Fatal("expected stop accepting before wait")
	}
	hub.WaitPending()
	hub.Shutdown()
}

func TestPoolBounded(t *testing.T) {
	t.Parallel()
	p := NewPool(2)
	var running int32
	for i := 0; i < 4; i++ {
		p.Submit(func() {
			atomic.AddInt32(&running, 1)
			time.Sleep(50 * time.Millisecond)
			atomic.AddInt32(&running, -1)
		})
	}
	time.Sleep(10 * time.Millisecond)
	if atomic.LoadInt32(&running) > 2 {
		t.Fatalf("too many concurrent jobs: %d", running)
	}
	p.Stop()
}
