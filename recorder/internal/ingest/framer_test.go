package ingest

import (
	"encoding/binary"
	"io"
	"net"
	"testing"
	"time"

	"github.com/shadow-diff/recorder/internal/beru"
	"github.com/shadow-diff/recorder/internal/config"
)

func writeFrame(w io.Writer, dir byte, payload []byte) error {
	hdr := make([]byte, FrameHeaderSize)
	hdr[0] = dir
	binary.BigEndian.PutUint32(hdr[1:5], uint32(len(payload)))
	if _, err := w.Write(hdr); err != nil {
		return err
	}
	if len(payload) > 0 {
		_, err := w.Write(payload)
		return err
	}
	return nil
}

func TestHandleConn_unexpectedEOF_noPanic(t *testing.T) {
	client, server := net.Pipe()
	store := NewSessionStore(beru.NewClient("http://127.0.0.1:1"), nil, 30*time.Second, DefaultMaxFrame)
	defer store.Stop()

	connID := store.RegisterConn()
	done := make(chan struct{})
	go func() {
		HandleConn(server, store, connID)
		close(done)
	}()

	// Partial header only
	_, _ = client.Write([]byte{DirRequest, 0, 0, 0, 5})
	_ = client.Close()
	<-done

	if store.SessionCount() != 0 {
		t.Fatalf("expected 0 sessions after EOF, got %d", store.SessionCount())
	}
}

func TestHandleConn_truncatedPayload_discards(t *testing.T) {
	client, server := net.Pipe()
	store := NewSessionStore(beru.NewClient("http://127.0.0.1:1"), nil, 30*time.Second, DefaultMaxFrame)
	defer store.Stop()

	connID := store.RegisterConn()
	go HandleConn(server, store, connID)

	hdr := []byte{DirRequest, 0, 0, 0, 10}
	_, _ = client.Write(hdr)
	_, _ = client.Write([]byte("short"))
	_ = client.Close()
	time.Sleep(100 * time.Millisecond)

	if store.SessionCount() != 0 {
		t.Fatalf("expected session discarded, count=%d", store.SessionCount())
	}
}

func TestHandleConn_requestOnlyThenClose(t *testing.T) {
	client, server := net.Pipe()
	store := NewSessionStore(beru.NewClient("http://127.0.0.1:1"), []config.Downstream{{Host: "api.example.com"}}, 30*time.Second, DefaultMaxFrame)
	defer store.Stop()

	connID := store.RegisterConn()
	go HandleConn(server, store, connID)

	_ = writeFrame(client, DirRequest, []byte("GET / HTTP/1.1\r\nHost: api.example.com\r\n\r\n"))
	_ = client.Close()
	time.Sleep(50 * time.Millisecond)
	store.FinishConn(connID)
}
