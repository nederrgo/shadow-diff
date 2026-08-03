package replay

import (
	"log/slog"
	"net"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shadow-diff/igris/internal/payload"
)

func TestSendOneWithRetry_SucceedsAfterRefused(t *testing.T) {
	var hits atomic.Int32
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	var sleeps []time.Duration
	var srv *http.Server
	oldSleep := sleep
	sleep = func(d time.Duration) {
		sleeps = append(sleeps, d)
		// After the second backoff, open the listener so attempt 3 succeeds.
		if len(sleeps) == 2 && srv == nil {
			srv = &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				hits.Add(1)
				w.WriteHeader(http.StatusOK)
			})}
			l2, err := net.Listen("tcp", addr)
			if err != nil {
				t.Errorf("relisten: %v", err)
				return
			}
			go func() { _ = srv.Serve(l2) }()
		}
	}
	t.Cleanup(func() {
		sleep = oldSleep
		if srv != nil {
			_ = srv.Close()
		}
	})

	res := sendOneWithRetry(
		&http.Client{Timeout: time.Second},
		slog.Default(),
		http.MethodPost,
		"/publish",
		http.Header{},
		[]byte(`{}`),
		payload.Target{Name: "control-a", BaseURL: "http://" + addr},
	)
	if res.Err != nil {
		t.Fatalf("err=%v after retries", res.Err)
	}
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", res.StatusCode)
	}
	if hits.Load() < 1 {
		t.Fatal("server never got a request")
	}
	if len(sleeps) != 2 {
		t.Fatalf("sleeps=%v want [1s 3s]", sleeps)
	}
	if sleeps[0] != time.Second || sleeps[1] != 3*time.Second {
		t.Fatalf("sleeps=%v want 1s then 3s", sleeps)
	}
}

func TestSendOneWithRetry_NoRetryOnHTTPStatus(t *testing.T) {
	var hits atomic.Int32
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		http.Error(w, "nope", http.StatusBadGateway)
	})}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })

	var slept int
	oldSleep := sleep
	sleep = func(time.Duration) { slept++ }
	t.Cleanup(func() { sleep = oldSleep })

	res := sendOneWithRetry(
		http.DefaultClient,
		slog.Default(),
		http.MethodGet,
		"/",
		http.Header{},
		nil,
		payload.Target{Name: "control-b", BaseURL: "http://" + ln.Addr().String()},
	)
	if res.Err != nil {
		t.Fatalf("unexpected transport err: %v", res.Err)
	}
	if res.StatusCode != http.StatusBadGateway {
		t.Fatalf("status=%d", res.StatusCode)
	}
	if hits.Load() != 1 || slept != 0 {
		t.Fatalf("hits=%d slept=%d want 1 hit and no retry", hits.Load(), slept)
	}
}

func TestSendOneWithRetry_ExhaustsBackoff(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	var sleeps []time.Duration
	oldSleep := sleep
	sleep = func(d time.Duration) { sleeps = append(sleeps, d) }
	t.Cleanup(func() { sleep = oldSleep })

	res := sendOneWithRetry(
		&http.Client{Timeout: 200 * time.Millisecond},
		slog.Default(),
		http.MethodGet,
		"/",
		nil,
		nil,
		payload.Target{Name: "candidate", BaseURL: "http://" + addr},
	)
	if res.Err == nil {
		t.Fatal("expected dial error")
	}
	want := []time.Duration{time.Second, 3 * time.Second, 5 * time.Second}
	if len(sleeps) != 3 || sleeps[0] != want[0] || sleeps[1] != want[1] || sleeps[2] != want[2] {
		t.Fatalf("sleeps=%v want %v", sleeps, want)
	}
}
