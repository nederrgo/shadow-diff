package http

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/shadow-diff/igris/internal/config"
	"github.com/shadow-diff/igris/internal/core"
	"github.com/shadow-diff/igris/internal/payload"
)

const testMaxBodySize = 512 * 1024

func testConfig(targets ...*httptest.Server) config.Config {
	return config.Config{
		ControlAURL:    targets[0].URL,
		ControlBURL:    targets[1].URL,
		CandidateURL:   targets[2].URL,
		ControlAAddr:   "127.0.0.1",
		ControlBAddr:   "127.0.0.1",
		CandidateAddr:  "127.0.0.1",
		WorkerPoolSize: 4,
		MaxTCPConns:    16,
		TCPDialTimeout: time.Second,
		TCPIdleTimeout: time.Minute,
	}
}

func TestTransformInjectsTraceID(t *testing.T) {
	t.Parallel()
	d := New(testMaxBodySize)
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set(HeaderShadowTraceID, "trace-abc")
	sess := &Session{Request: req, Body: nil}
	meta, err := d.ParseMetadata(sess)
	if err != nil {
		t.Fatal(err)
	}
	msg, err := d.Transform(sess, meta)
	if err != nil {
		t.Fatal(err)
	}
	hm := msg.(*message)
	if got := hm.headers.Get(HeaderShadowTraceID); got != "trace-abc" {
		t.Fatalf("header trace = %q, want trace-abc", got)
	}
}

func TestMulticastCloneFidelityAndTraceOnAllTargets(t *testing.T) {
	t.Parallel()

	type captured struct {
		method     string
		requestURI string
		body       string
		traceID    string
	}
	var mu sync.Mutex
	captures := make([]captured, 0, 3)

	servers := make([]*httptest.Server, 3)
	for i := range servers {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			mu.Lock()
			captures = append(captures, captured{
				method:     r.Method,
				requestURI: r.URL.RequestURI(),
				body:       string(b),
				traceID:    r.Header.Get(HeaderShadowTraceID),
			})
			mu.Unlock()
			w.WriteHeader(http.StatusOK)
		}))
		servers[i] = srv
		defer srv.Close()
	}

	cfg := testConfig(servers...)
	hub := core.NewHub(cfg, slog.Default())

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/items?q=1", bytes.NewReader([]byte(`{"ok":true}`)))
	req.Header.Set(HeaderShadowTraceID, "trace-fixed")
	body, _ := io.ReadAll(req.Body)
	_ = req.Body.Close()
	if err := hub.HandleAtomic(New(testMaxBodySize), &Session{Request: req, Body: body, Writer: rec}); err != nil {
		t.Fatal(err)
	}
	hub.WaitPendingAtomic()

	mu.Lock()
	defer mu.Unlock()
	if len(captures) != 3 {
		t.Fatalf("got %d captures, want 3", len(captures))
	}
	trace := "trace-fixed"
	for _, c := range captures {
		if c.traceID != trace {
			t.Fatalf("mismatched trace ids: %q vs %q", c.traceID, trace)
		}
		if c.method != http.MethodPost {
			t.Fatalf("method = %q", c.method)
		}
		if c.requestURI != "/api/items?q=1" {
			t.Fatalf("uri = %q", c.requestURI)
		}
	}
}

func TestTransformRedactsHeaders(t *testing.T) {
	t.Parallel()
	d := New(testMaxBodySize)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Cookie", "a=b")
	req.Header.Set("X-Keep", "yes")
	sess := &Session{Request: req}
	meta, _ := d.ParseMetadata(sess)
	msg, err := d.Transform(sess, meta)
	if err != nil {
		t.Fatal(err)
	}
	hm := msg.(*message)
	for _, h := range []string{"Authorization", "Cookie", "Proxy-Authorization"} {
		if hm.headers.Get(h) != "" {
			t.Fatalf("outbound has %q", h)
		}
	}
	if hm.headers.Get("X-Keep") != "yes" {
		t.Fatal("X-Keep stripped")
	}
	if req.Header.Get("Authorization") != "Bearer secret" {
		t.Fatal("Transform mutated original request headers")
	}
}

func TestHandlerReturns202WithTrace(t *testing.T) {
	t.Parallel()

	backendTrace := make(chan string, 3)
	mkBackend := func() *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			backendTrace <- r.Header.Get(HeaderShadowTraceID)
			w.WriteHeader(http.StatusOK)
		}))
	}
	backend := mkBackend()
	defer backend.Close()
	s2 := mkBackend()
	defer s2.Close()
	s3 := mkBackend()
	defer s3.Close()

	cfg := config.Config{
		ControlAURL:    backend.URL,
		ControlBURL:    s2.URL,
		CandidateURL:   s3.URL,
		ControlAAddr:   "127.0.0.1",
		ControlBAddr:   "127.0.0.1",
		CandidateAddr:  "127.0.0.1",
		WorkerPoolSize: 2,
		MaxTCPConns:    8,
		TCPDialTimeout: time.Second,
		TCPIdleTimeout: time.Minute,
	}
	hub := core.NewHub(cfg, slog.Default())
	d := New(testMaxBodySize)
	d.Client = backend.Client()

	mux := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()
		sess := &Session{Request: r, Body: body, Writer: w}
		_ = hub.HandleAtomic(d, sess)
	}))
	defer mux.Close()

	resp, err := http.Get(mux.URL + "/hello")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status %d", resp.StatusCode)
	}
	respTrace := resp.Header.Get(HeaderShadowTraceID)
	if respTrace == "" {
		t.Fatal("missing trace on 202")
	}
	hub.WaitPendingAtomic()
	for i := 0; i < 3; i++ {
		select {
		case got := <-backendTrace:
			if got != respTrace {
				t.Fatalf("backend %q != response %q", got, respTrace)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("timeout waiting for backend %d", i)
		}
	}
}

func TestHandlerRejectsOversizedBody(t *testing.T) {
	t.Parallel()

	hits := make(chan struct{}, 3)
	mkBackend := func() *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hits <- struct{}{}
			w.WriteHeader(http.StatusOK)
		}))
	}
	s1, s2, s3 := mkBackend(), mkBackend(), mkBackend()
	defer s1.Close()
	defer s2.Close()
	defer s3.Close()

	const maxBody = 1024
	cfg := config.Config{
		ControlAURL:    s1.URL,
		ControlBURL:    s2.URL,
		CandidateURL:   s3.URL,
		ControlAAddr:   "127.0.0.1",
		ControlBAddr:   "127.0.0.1",
		CandidateAddr:  "127.0.0.1",
		WorkerPoolSize: 2,
		MaxTCPConns:    8,
		TCPDialTimeout: time.Second,
		TCPIdleTimeout: time.Minute,
	}
	hub := core.NewHub(cfg, slog.Default())
	d := New(maxBody)
	d.Client = s1.Client()

	srv := httptest.NewServer(d.handler(hub))
	defer srv.Close()

	oversized := bytes.Repeat([]byte("x"), maxBody+1)
	resp, err := http.Post(srv.URL+"/", "application/json", bytes.NewReader(oversized))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("status %d, want 413", resp.StatusCode)
	}

	select {
	case <-hits:
		t.Fatal("backend received request for oversized body")
	default:
	}
}

func TestDispatchUsesDetachedContext(t *testing.T) {
	t.Parallel()

	received := make(chan struct{}, 3)
	servers := make([]*httptest.Server, 3)
	for i := range servers {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			received <- struct{}{}
			w.WriteHeader(http.StatusOK)
		}))
		servers[i] = srv
		defer srv.Close()
	}

	msg := &message{
		method:     http.MethodGet,
		requestURI: "/test",
		headers:    http.Header{HeaderShadowTraceID: []string{"t1"}},
		client:     servers[0].Client(),
	}
	targets := []payload.Target{
		{Name: "a", BaseURL: servers[0].URL},
		{Name: "b", BaseURL: servers[1].URL},
		{Name: "c", BaseURL: servers[2].URL},
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_ = ctx
	results := msg.Dispatch(context.Background(), targets)
	if len(results) != 3 {
		t.Fatalf("got %d results", len(results))
	}
	for i := 0; i < 3; i++ {
		select {
		case <-received:
		case <-time.After(5 * time.Second):
			t.Fatalf("only %d received", i)
		}
	}
}
