package ingest

import (
	"testing"
	"time"

	"github.com/shadow-diff/recorder/internal/beru"
)

func TestSessionStore_buffersUntilFinishConn(t *testing.T) {
	store := NewSessionStore(beru.NewClient("http://127.0.0.1:1"), nil, 30*time.Second, DefaultMaxFrame)
	defer store.Stop()

	connID := store.RegisterConn()
	_ = store.WriteFrame(connID, DirRequest, []byte("POST /x HTTP/1.1\r\nHost: a\r\n\r\n"))
	_ = store.WriteFrame(connID, DirResponse, []byte("HTTP/1.1 200 OK\r\n\r\n"))
	_ = store.WriteFrame(connID, DirResponse, []byte(`{"ok":true}`))

	store.mu.Lock()
	sess := store.sessions[connID]
	if sess.paired {
		store.mu.Unlock()
		t.Fatal("should not pair before FinishConn")
	}
	if len(sess.resBuf) != len("HTTP/1.1 200 OK\r\n\r\n")+len(`{"ok":true}`) {
		store.mu.Unlock()
		t.Fatalf("resBuf len=%d", len(sess.resBuf))
	}
	store.mu.Unlock()

	store.FinishConn(connID)
	time.Sleep(30 * time.Millisecond)
}
