package ingest

import (
	"testing"
	"time"

	"github.com/shadow-diff/recorder/internal/beru"
)

func TestSessionStore_pairTimeoutEviction(t *testing.T) {
	store := NewSessionStore(beru.NewClient("http://127.0.0.1:1"), nil, 50*time.Millisecond, DefaultMaxFrame)
	defer store.Stop()

	connID := store.RegisterConn()
	_ = store.WriteFrame(connID, DirRequest, []byte("partial request bytes"))

	time.Sleep(200 * time.Millisecond)
	store.evictExpired()

	if store.SessionCount() != 0 {
		t.Fatalf("expected session evicted after pair timeout, count=%d", store.SessionCount())
	}
}

func TestSessionStore_orphanResponseDiscarded(t *testing.T) {
	store := NewSessionStore(beru.NewClient("http://127.0.0.1:1"), nil, 30*time.Second, DefaultMaxFrame)
	defer store.Stop()

	connID := store.RegisterConn()
	err := store.WriteFrame(connID, DirResponse, []byte("HTTP/1.1 200 OK\r\n\r\n"))
	if err != nil {
		t.Fatal(err)
	}
	// Session remains but no req bytes — no parser started
	if store.SessionCount() != 1 {
		t.Fatalf("session count: %d", store.SessionCount())
	}
}
