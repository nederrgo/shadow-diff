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

func TestSessionStore_orphanResponseBufferedUntilRequest(t *testing.T) {
	store := NewSessionStore(beru.NewClient("http://127.0.0.1:1"), nil, 30*time.Second, DefaultMaxFrame)
	defer store.Stop()

	connID := store.RegisterConn()
	resp := []byte("HTTP/1.1 200 OK\r\n\r\n")
	if err := store.WriteFrame(connID, DirResponse, resp); err != nil {
		t.Fatal(err)
	}
	if store.SessionCount() != 1 {
		t.Fatalf("session count: %d", store.SessionCount())
	}
	req := []byte("POST /v1/log HTTP/1.1\r\nHost: example.com\r\n\r\n{}")
	if err := store.WriteFrame(connID, DirRequest, req); err != nil {
		t.Fatal(err)
	}
	store.FinishConn(connID)
	time.Sleep(50 * time.Millisecond)
}
