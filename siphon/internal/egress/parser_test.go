package egress

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shadow-diff/siphon/internal/config"
)

// keepAliveFixture is two HTTP/1.1 transactions on one connection (request leg + response leg).
func keepAliveFixture() (reqBytes, resBytes string) {
	// Bodies are exactly Content-Length bytes; no extra CRLF between back-to-back messages.
	reqBytes = strings.Join([]string{
		"POST /first HTTP/1.1\r\n",
		"Host: api.example.com\r\n",
		"Content-Length: 2\r\n",
		"Connection: keep-alive\r\n",
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

func TestParseBidirectionalStream_keepAlive(t *testing.T) {
	var (
		mu      sync.Mutex
		records []RecordPayload
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/record_egress" {
			http.NotFound(w, r)
			return
		}
		var rec RecordPayload
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

	beruHost := strings.TrimPrefix(srv.URL, "http://")
	target := &config.SiphonTarget{
		BeruHTTPHost: beruHost,
		Downstreams: []config.SiphonDownstream{
			{Host: "api.example.com", IgnorePaths: []string{"$.timestamp"}},
		},
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

	sess := &Session{
		reqR: reqR,
		resR: resR,
		Target: target,
	}
	forward := NewForwarder()
	ParseBidirectionalStream(sess, forward)

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
	if records[0].Path != "/first" || records[0].Response.Status != 200 {
		t.Fatalf("first record: %+v", records[0])
	}
	if records[1].Path != "/second" || records[1].Response.Status != 201 {
		t.Fatalf("second record: %+v", records[1])
	}
	if records[0].IgnorePaths == nil || records[0].IgnorePaths[0] != "$.timestamp" {
		t.Fatalf("expected ignore_paths on record: %+v", records[0].IgnorePaths)
	}
}

func TestConfigHostMatches_wildcard(t *testing.T) {
	ds := []config.SiphonDownstream{{Host: "*.example.com"}}
	if !configHostMatches("api.example.com", ds) {
		t.Fatal("expected wildcard match")
	}
	if configHostMatches("other.org", ds) {
		t.Fatal("expected no match")
	}
}
