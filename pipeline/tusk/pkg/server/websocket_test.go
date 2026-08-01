package server

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/shadow-diff/tusk/pkg/topology"
)

func testServer(t *testing.T) (*Hub, string) {
	t.Helper()
	hub := NewHub()
	h := &HTTPServer{Hub: hub, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	srv := httptest.NewServer(h.Handler())
	t.Cleanup(srv.Close)
	return hub, "ws" + strings.TrimPrefix(srv.URL, "http")
}

func dial(t *testing.T, wsURL string) *websocket.Conn {
	t.Helper()
	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial %s: %v (resp %v)", wsURL, err, resp)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func readGraph(t *testing.T, conn *websocket.Conn) *topology.TopologyGraph {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	_, data, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var g topology.TopologyGraph
	if err := json.Unmarshal(data, &g); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return &g
}

func graph(namespace, name string) *topology.TopologyGraph {
	return &topology.TopologyGraph{TestName: name, Namespace: namespace, Phase: "Ready"}
}

// A browser attaching to an already-converged ShadowTest must get the cached
// graph immediately; Monarch will not send another update until something changes.
func TestWebSocket_SendsCachedGraphOnConnect(t *testing.T) {
	hub, wsURL := testServer(t)
	hub.Broadcast(graph("default", "alpha"))

	conn := dial(t, wsURL+"/ws/monitor?test=alpha&namespace=default")

	got := readGraph(t, conn)
	if got.TestName != "alpha" || got.Namespace != "default" {
		t.Fatalf("cached graph = %s/%s", got.Namespace, got.TestName)
	}
}

func TestWebSocket_StreamsLiveUpdates(t *testing.T) {
	hub, wsURL := testServer(t)
	conn := dial(t, wsURL+"/ws/monitor?test=alpha&namespace=default")

	// Wait for the subscription to land before broadcasting, otherwise the update
	// races the Subscribe call and the test flakes.
	waitForClients(t, hub, 1)
	hub.Broadcast(graph("default", "alpha"))

	if got := readGraph(t, conn); got.TestName != "alpha" {
		t.Fatalf("live graph = %q", got.TestName)
	}
}

func TestWebSocket_FiltersByTest(t *testing.T) {
	hub, wsURL := testServer(t)
	hub.Broadcast(graph("default", "beta"))
	hub.Broadcast(graph("default", "alpha"))

	conn := dial(t, wsURL+"/ws/monitor?test=alpha&namespace=default")

	got := readGraph(t, conn)
	if got.TestName != "alpha" {
		t.Fatalf("filter leaked: %q", got.TestName)
	}
	// No second frame should follow: beta must not reach this client.
	if err := conn.SetReadDeadline(time.Now().Add(300 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if _, data, err := conn.ReadMessage(); err == nil {
		t.Fatalf("unexpected extra frame: %s", data)
	}
}

func TestHub_DeletedEvictsCacheAndNotifies(t *testing.T) {
	hub, wsURL := testServer(t)
	hub.Broadcast(graph("default", "alpha"))

	conn := dial(t, wsURL+"/ws/monitor?test=alpha&namespace=default")
	if got := readGraph(t, conn); got.Phase != "Ready" {
		t.Fatalf("cached = %q", got.Phase)
	}

	waitForClients(t, hub, 1)
	hub.Broadcast(&topology.TopologyGraph{TestName: "alpha", Namespace: "default", Phase: "Deleted"})

	if got := readGraph(t, conn); got.Phase != "Deleted" {
		t.Fatalf("tombstone = %q", got.Phase)
	}

	// A new subscriber must not receive the deleted test from cache.
	conn2 := dial(t, wsURL+"/ws/monitor?test=alpha&namespace=default")
	if err := conn2.SetReadDeadline(time.Now().Add(300 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if _, data, err := conn2.ReadMessage(); err == nil {
		t.Fatalf("deleted test still cached: %s", data)
	}
}

func TestWebSocket_NoFilterWatchesAllTests(t *testing.T) {
	hub, wsURL := testServer(t)
	hub.Broadcast(graph("default", "alpha"))
	hub.Broadcast(graph("other", "beta"))

	conn := dial(t, wsURL+"/ws/monitor")

	seen := map[string]bool{}
	for i := 0; i < 2; i++ {
		seen[readGraph(t, conn).TestName] = true
	}
	if !seen["alpha"] || !seen["beta"] {
		t.Fatalf("expected both tests, saw %v", seen)
	}
}

func TestWebSocket_DisconnectUnsubscribes(t *testing.T) {
	hub, wsURL := testServer(t)
	conn := dial(t, wsURL+"/ws/monitor")
	waitForClients(t, hub, 1)

	_ = conn.Close()
	waitForClients(t, hub, 0)
}

func TestHealthz(t *testing.T) {
	_, wsURL := testServer(t)
	httpURL := "http" + strings.TrimPrefix(wsURL, "ws")

	resp, err := http.Get(httpURL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != defaultCORSOrigin {
		t.Fatalf("CORS header = %q, want %q", got, defaultCORSOrigin)
	}
}

func TestAllowedOrigin(t *testing.T) {
	cases := []struct {
		origin string
		want   bool
	}{
		{"http://localhost:3000", true},
		{"http://localhost", true},
		{"http://localhost:80", true},
		{"https://127.0.0.1:8443", true},
		{"http://evil.example.com", false},
		{"ftp://localhost", false},
		{"not-a-url", false},
	}
	for _, tc := range cases {
		if got := allowedOrigin(tc.origin); got != tc.want {
			t.Fatalf("allowedOrigin(%q) = %v, want %v", tc.origin, got, tc.want)
		}
	}
}

func TestWebSocket_AcceptsLocalhostPort80(t *testing.T) {
	_, wsURL := testServer(t)

	header := http.Header{}
	header.Set("Origin", "http://localhost:80")
	conn, _, err := websocket.DefaultDialer.Dial(wsURL+"/ws/monitor", header)
	if err != nil {
		t.Fatalf("localhost:80 origin should be accepted: %v", err)
	}
	_ = conn.Close()
}

func TestWebSocket_RejectsForeignOrigin(t *testing.T) {
	_, wsURL := testServer(t)

	header := http.Header{}
	header.Set("Origin", "http://evil.example.com")
	conn, resp, err := websocket.DefaultDialer.Dial(wsURL+"/ws/monitor", header)
	if err == nil {
		_ = conn.Close()
		t.Fatal("upgrade from a foreign origin should be rejected")
	}
	if resp != nil && resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.StatusCode)
	}
}

func waitForClients(t *testing.T, h *Hub, want int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		h.mu.RLock()
		n := len(h.clients)
		h.mu.RUnlock()
		if n == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("client count never reached %d", want)
}
