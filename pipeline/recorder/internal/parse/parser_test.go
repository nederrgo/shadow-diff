package parse

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shadow-diff/recorder/internal/beru"
	"github.com/shadow-diff/recorder/internal/config"
)

const keepAliveTraceID = "4bf92f3577b34da6a3ce929d0e0e4736"

func keepAliveFixture() (reqBytes, resBytes string) {
	reqBytes = strings.Join([]string{
		"POST /first HTTP/1.1\r\n",
		"Host: api.example.com\r\n",
		"Content-Length: 2\r\n",
		"Connection: keep-alive\r\n",
		"Traceparent: 00-" + keepAliveTraceID + "-00f067aa0ba902b7-01\r\n",
		"\r\n",
		"{}",
		"POST /second HTTP/1.1\r\n",
		"Host: api.example.com\r\n",
		"Content-Length: 2\r\n",
		"Connection: close\r\n",
		"\r\n",
		"{}",
	}, "")
	resBytes = strings.Join([]string{
		"HTTP/1.1 200 OK\r\n",
		"Content-Length: 3\r\n",
		"Connection: keep-alive\r\n",
		"\r\n",
		"ok1",
		"HTTP/1.1 201 Created\r\n",
		"Content-Length: 3\r\n",
		"Connection: close\r\n",
		"\r\n",
		"ok2",
	}, "")
	return reqBytes, resBytes
}

func TestRunBidirectional_keepAlive(t *testing.T) {
	var (
		mu      sync.Mutex
		records []beru.RecordPayload
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/record_egress" {
			http.NotFound(w, r)
			return
		}
		var rec beru.RecordPayload
		if err := json.NewDecoder(r.Body).Decode(&rec); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		mu.Lock()
		records = append(records, rec)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"hash":"test"}`))
	}))
	t.Cleanup(srv.Close)

	client := beru.NewClient(srv.URL)
	recordAndReplay := []config.RecordAndReplayHost{
		{Host: "api.example.com", IgnorePaths: []string{"$.timestamp"}},
	}

	reqR, reqW := io.Pipe()
	resR, resW := io.Pipe()
	reqPayload, resPayload := keepAliveFixture()

	go func() {
		_, _ = io.WriteString(reqW, reqPayload)
		_ = reqW.Close()
	}()
	go func() {
		_, _ = io.WriteString(resW, resPayload)
		_ = resW.Close()
	}()

	RunBidirectional(context.Background(), reqR, resR, recordAndReplay, client)

	deadline := time.Now().Add(3 * time.Second)
	for {
		mu.Lock()
		n := len(records)
		mu.Unlock()
		if n >= 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("expected 2 recorded transactions, got %d", n)
		}
		time.Sleep(50 * time.Millisecond)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(records) != 2 {
		t.Fatalf("record count: got %d want 2", len(records))
	}
	byPath := map[string]beru.RecordPayload{}
	for _, rec := range records {
		byPath[rec.Path] = rec
	}
	if byPath["/first"].Response.Status != 200 || byPath["/second"].Response.Status != 201 {
		t.Fatalf("records by path: %+v", byPath)
	}
	// The first request had a traceparent header — verify trace ID was extracted.
	if byPath["/first"].TraceID != keepAliveTraceID {
		t.Fatalf("expected traceID %q on /first, got %q", keepAliveTraceID, byPath["/first"].TraceID)
	}
	// The second request had no traceparent — trace ID should be empty.
	if byPath["/second"].TraceID != "" {
		t.Fatalf("expected empty traceID on /second, got %q", byPath["/second"].TraceID)
	}
}

func TestTraceIDFromTraceparent(t *testing.T) {
	cases := []struct {
		header string
		want   string
	}{
		{"00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01", "4bf92f3577b34da6a3ce929d0e0e4736"},
		{"", ""},
		{"bad", ""},
		{"00-short-00f067aa0ba902b7-01", ""},
	}
	for _, tc := range cases {
		if got := traceIDFromTraceparent(tc.header); got != tc.want {
			t.Errorf("traceIDFromTraceparent(%q) = %q, want %q", tc.header, got, tc.want)
		}
	}
}

func TestHostMatches_wildcard(t *testing.T) {
	ds := []config.RecordAndReplayHost{{Host: "*.example.com"}}
	if !HostMatches("api.example.com", ds) {
		t.Fatal("expected wildcard match")
	}
	if HostMatches("other.org", ds) {
		t.Fatal("expected no match")
	}
}
