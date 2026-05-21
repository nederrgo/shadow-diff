package tcpstream

import (
	"context"
	"log/slog"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/shadow-diff/igris/internal/config"
	"github.com/shadow-diff/igris/internal/core"
)

func testHub(idle time.Duration, maxConns int) *core.Hub {
	cfg := config.Config{
		ControlAURL:    "http://127.0.0.1:1",
		ControlBURL:    "http://127.0.0.1:2",
		CandidateURL:   "http://127.0.0.1:3",
		ControlAAddr:   "127.0.0.1",
		ControlBAddr:   "127.0.0.1",
		CandidateAddr:  "127.0.0.1",
		WorkerPoolSize: 2,
		MaxTCPConns:    maxConns,
		TCPDialTimeout: 2 * time.Second,
		TCPIdleTimeout: idle,
	}
	return core.NewHub(cfg, slog.Default())
}

func TestTCPMulticastFanOut(t *testing.T) {
	t.Parallel()

	backend, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	backendPort := backend.Addr().(*net.TCPAddr).Port

	var received sync.WaitGroup
	received.Add(3)
	go func() {
		for i := 0; i < 3; i++ {
			conn, err := backend.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				buf := make([]byte, 8)
				n, _ := c.Read(buf)
				if string(buf[:n]) == "ping" {
					received.Done()
				}
			}(conn)
		}
	}()

	hub := testHub(time.Minute, 8)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	client, server := net.Pipe()
	hub.RelayTCP(ctx, server, backendPort)

	_, _ = client.Write([]byte("ping"))
	_ = client.Close()

	done := make(chan struct{})
	go func() {
		received.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("targets did not receive data")
	}
	hub.WaitPendingStreams()
}

func TestTCPIdleTimeoutClosesRelay(t *testing.T) {
	t.Parallel()

	backend, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	backendPort := backend.Addr().(*net.TCPAddr).Port

	go func() {
		for {
			c, err := backend.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()

	hub := testHub(50*time.Millisecond, 8)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	client, server := net.Pipe()
	hub.RelayTCP(ctx, server, backendPort)

	time.Sleep(200 * time.Millisecond)
	hub.WaitPendingStreams()
	_ = client.Close()
}

func TestTCPConnectionLimit(t *testing.T) {
	t.Parallel()

	backend, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	backendPort := backend.Addr().(*net.TCPAddr).Port

	go func() {
		for {
			c, err := backend.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				buf := make([]byte, 64)
				_, _ = conn.Read(buf)
				_ = conn.Close()
			}(c)
		}
	}()

	hub := testHub(time.Minute, 1)
	ctx := context.Background()

	c1, s1 := net.Pipe()
	hub.RelayTCP(ctx, s1, backendPort)
	time.Sleep(50 * time.Millisecond)

	// Second relay should be rejected while the first holds the semaphore.
	_, s2 := net.Pipe()
	hub.RelayTCP(ctx, s2, backendPort)
	time.Sleep(20 * time.Millisecond)

	_, _ = c1.Write([]byte("wake"))
	_ = c1.Close()
	hub.WaitPendingStreams()
}
