package export

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gopacket/gopacket"
	"github.com/gopacket/gopacket/layers"
	"net"
)

const (
	targetIP = "10.0.0.5"
	peerIP   = "10.0.0.9"
	traceHdr = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	traceID  = "4bf92f3577b34da6a3ce929d0e0e4736"
)

// flows builds a request-direction flow pair from src to dst.
func flows(t *testing.T, src, dst string) (gopacket.Flow, gopacket.Flow) {
	t.Helper()
	netFlow, err := gopacket.FlowFromEndpoints(
		layers.NewIPEndpoint(mustIP(t, src)), layers.NewIPEndpoint(mustIP(t, dst)))
	if err != nil {
		t.Fatalf("net flow: %v", err)
	}
	transFlow, err := gopacket.FlowFromEndpoints(
		layers.NewTCPPortEndpoint(40001), layers.NewTCPPortEndpoint(8080))
	if err != nil {
		t.Fatalf("transport flow: %v", err)
	}
	return netFlow, transFlow
}

func mustIP(t *testing.T, s string) []byte {
	t.Helper()
	ip := net.ParseIP(s)
	if ip == nil {
		t.Fatalf("bad IP %q", s)
	}
	return ip
}

// exporterWithShop wires an Exporter to a stub Shop and returns the recorded
// payloads channel.
func exporterWithShop(t *testing.T, egressURL, igrisURL string) (*Exporter, chan EgressRecord, *httptest.Server) {
	t.Helper()
	seen := make(chan EgressRecord, 8)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/record_egress" {
			t.Errorf("path = %q, want /v1/record_egress", r.URL.Path)
		}
		var rec EgressRecord
		if err := json.NewDecoder(r.Body).Decode(&rec); err != nil {
			t.Errorf("decode: %v", err)
		}
		seen <- rec
		_ = json.NewEncoder(w).Encode(map[string]string{"hash": "trace:" + rec.TraceID})
	}))
	t.Cleanup(srv.Close)

	if egressURL == "useServer" {
		egressURL = srv.URL
	}
	router := NewRouter(slog.Default())
	router.Rebuild([]RuleExport{{
		Key: "ns/rule", IPs: []string{targetIP},
		IgrisBaseURL: igrisURL, EgressBaseURL: egressURL, SamplePercentage: 100,
	}})
	e := NewExporter(router, 1, 8, slog.Default())
	t.Cleanup(e.Stop)
	return e, seen, srv
}

func txnReq(t *testing.T, method, uri, host, traceparent string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(method, "http://"+host+uri, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.RequestURI = uri
	req.Host = host
	if traceparent != "" {
		req.Header.Set("traceparent", traceparent)
	}
	return req
}

func txnResp(status string, code int, hdrs map[string]string, body string) *http.Response {
	h := http.Header{}
	for k, v := range hdrs {
		h.Set(k, v)
	}
	return &http.Response{
		Status: status, StatusCode: code, Header: h,
		Body: io.NopCloser(strings.NewReader(body)),
	}
}

// Egress joins on the SOURCE address; ingress joins on the destination. One
// table serves both.
func TestHandleTransactionRecordsEgressBySourceMatch(t *testing.T) {
	e, seen, _ := exporterWithShop(t, "useServer", "http://igris:8080")
	netFlow, transFlow := flows(t, targetIP, peerIP)

	e.HandleTransaction(netFlow, transFlow,
		txnReq(t, "GET", "/v1/users?active=true", "api.Example.com:8443", traceHdr),
		txnResp("200 OK", 200, map[string]string{"Content-Type": "application/json"}, `{"ok":true}`))

	select {
	case rec := <-seen:
		if rec.TraceID != traceID {
			t.Errorf("TraceID = %q, want %q", rec.TraceID, traceID)
		}
		// Host and method go verbatim: Shop upper-cases and strips the port on
		// both the seed and the ext_proc lookup path, so normalizing here could
		// only create a key Envoy never generates.
		if rec.Host != "api.Example.com:8443" {
			t.Errorf("Host = %q, want the wire value unmodified", rec.Host)
		}
		if rec.Method != "GET" {
			t.Errorf("Method = %q, want GET", rec.Method)
		}
		// Envoy's :path includes the query, so the seeded key must too.
		if rec.Path != "/v1/users?active=true" {
			t.Errorf("Path = %q, want the query retained", rec.Path)
		}
		if rec.Response.Status != 200 || rec.Response.Body != `{"ok":true}` {
			t.Errorf("response = %+v", rec.Response)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no egress record reached shop")
	}
}

// A capture whose target is the destination is ingress, not egress; recording
// it as a mock would seed a dependency response the target never made.
func TestHandleTransactionIgnoresIngressDirection(t *testing.T) {
	e, seen, _ := exporterWithShop(t, "useServer", "http://igris:8080")
	netFlow, transFlow := flows(t, peerIP, targetIP)

	e.HandleTransaction(netFlow, transFlow,
		txnReq(t, "GET", "/in", "svc", traceHdr),
		txnResp("200 OK", 200, nil, "hi"))

	select {
	case rec := <-seen:
		t.Fatalf("ingress direction must not be recorded as egress: %+v", rec)
	case <-time.After(300 * time.Millisecond):
	}
}

// Tracing is required: an untraced call cannot be keyed and Shop rejects it.
func TestHandleTransactionDropsUntraced(t *testing.T) {
	e, seen, _ := exporterWithShop(t, "useServer", "")
	netFlow, transFlow := flows(t, targetIP, peerIP)

	for _, tp := range []string{"", "not-a-traceparent", "00-tooshort-00f067aa0ba902b7-01"} {
		e.HandleTransaction(netFlow, transFlow,
			txnReq(t, "GET", "/x", "svc", tp),
			txnResp("200 OK", 200, nil, "hi"))
	}
	select {
	case rec := <-seen:
		t.Fatalf("untraced egress must be dropped, got %+v", rec)
	case <-time.After(300 * time.Millisecond):
	}
}

// The sampling gate is the same one the replaced Recorder applied.
func TestHandleTransactionHonoursSampleGate(t *testing.T) {
	seen := make(chan EgressRecord, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var rec EgressRecord
		_ = json.NewDecoder(r.Body).Decode(&rec)
		seen <- rec
		_, _ = w.Write([]byte(`{"hash":"k"}`))
	}))
	defer srv.Close()

	router := NewRouter(slog.Default())
	// traceID starts "4b" = 75; 75*100 >= 1*256, so a 1% gate excludes it.
	router.Rebuild([]RuleExport{{
		Key: "ns/rule", IPs: []string{targetIP},
		EgressBaseURL: srv.URL, SamplePercentage: 1,
	}})
	e := NewExporter(router, 1, 8, slog.Default())
	defer e.Stop()

	netFlow, transFlow := flows(t, targetIP, peerIP)
	e.HandleTransaction(netFlow, transFlow,
		txnReq(t, "GET", "/x", "svc", traceHdr),
		txnResp("200 OK", 200, nil, "hi"))

	select {
	case rec := <-seen:
		t.Fatalf("sampled-out egress must be dropped, got %+v", rec)
	case <-time.After(300 * time.Millisecond):
	}
}

// Framing and nondeterministic headers must not reach a replayed mock.
func TestFilterResponseHeaders(t *testing.T) {
	h := http.Header{}
	for k, v := range map[string]string{
		"Content-Type":      "application/json",
		"Cache-Control":     "no-store",
		"ETag":              `"abc"`,
		"Location":          "/next",
		"X-Request-Id":      "r-1",
		"Content-Length":    "1043",
		"Transfer-Encoding": "chunked",
		"Connection":        "keep-alive",
		"Date":              "Tue, 25 Jul 2026 00:00:00 GMT",
		"Server":            "nginx",
		"Set-Cookie":        "a=b",
	} {
		h.Set(k, v)
	}

	got := filterResponseHeaders(h)
	for _, want := range []string{"content-type", "cache-control", "etag", "location", "x-request-id"} {
		if _, ok := got[want]; !ok {
			t.Errorf("header %q should be kept", want)
		}
	}
	for _, bad := range []string{"content-length", "transfer-encoding", "connection", "date", "server", "set-cookie"} {
		if v, ok := got[bad]; ok {
			t.Errorf("header %q should be dropped, got %q", bad, v)
		}
	}
}

// A rule that only records egress is still a usable rule; the old Rebuild
// dropped it because IgrisBaseURL was empty.
func TestRebuildKeepsEgressOnlyRule(t *testing.T) {
	r := NewRouter(slog.Default())
	r.Rebuild([]RuleExport{{Key: "ns/a", IPs: []string{targetIP}, EgressBaseURL: "http://shop:8080"}})

	route, ok := r.Lookup(targetIP)
	if !ok {
		t.Fatal("egress-only rule must register a route")
	}
	if route.EgressBaseURL != "http://shop:8080" || route.IgrisBaseURL != "" {
		t.Fatalf("route = %+v", route)
	}
}

// A rule with neither URL has nowhere to send anything.
func TestRebuildSkipsRuleWithNoURLs(t *testing.T) {
	r := NewRouter(slog.Default())
	r.Rebuild([]RuleExport{{Key: "ns/a", IPs: []string{targetIP}}})
	if _, ok := r.Lookup(targetIP); ok {
		t.Fatal("rule with no URLs should not register")
	}
}

// WantTransaction is defined on the REQUEST direction: for egress the target
// pod is the source. decode is responsible for reversing the response half's
// flow before asking -- see TestPairingGateUsesRequestDirectionForBothHalves.
func TestWantTransaction(t *testing.T) {
	r := NewRouter(slog.Default())
	r.Rebuild([]RuleExport{
		{Key: "ns/a", IPs: []string{targetIP}, EgressBaseURL: "http://shop:8080"},
		{Key: "ns/b", IPs: []string{peerIP}, IgrisBaseURL: "http://igris:8080"},
	})
	e := NewExporter(r, 1, 4, slog.Default())
	defer e.Stop()

	egressFlow, _ := flows(t, targetIP, "10.0.0.99")
	if !e.WantTransaction(egressFlow) {
		t.Error("a source with an egress route wants pairing")
	}
	ingressFlow, _ := flows(t, peerIP, "10.0.0.99")
	if e.WantTransaction(ingressFlow) {
		t.Error("a source with only an igris route must not want pairing")
	}
	unknownFlow, _ := flows(t, "10.0.0.77", "10.0.0.99")
	if e.WantTransaction(unknownFlow) {
		t.Error("an unknown source must not want pairing")
	}

	// The response half of that same egress connection runs the other way. Asked
	// with its own flow the answer is correctly false -- which is exactly why
	// decode must reverse it before asking, rather than this predicate trying to
	// accept either endpoint (that would also pair every ingress connection).
	responseHalf, _ := flows(t, "10.0.0.99", targetIP)
	if e.WantTransaction(responseHalf) {
		t.Error("the response direction must not match on its own; decode reverses it")
	}
	if !e.WantTransaction(responseHalf.Reverse()) {
		t.Error("reversed to the request direction, the same connection must want pairing")
	}
}
