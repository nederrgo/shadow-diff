package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Trace propagation is the one behaviour this app exists to provide. Kaisel
// keys every egress mock on the trace id, so an outbound call that loses the
// traceparent is dropped before Shop ever sees it -- and the egress test would
// fail with no indication that the app, not the capture, was at fault.
func TestOutboundCallPropagatesTraceContext(t *testing.T) {
	var got http.Header
	dep := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"dependency":"ok"}`))
	}))
	defer dep.Close()

	const tp = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/egress/get?path=/dep/echo", nil)
	req.Header.Set("traceparent", tp)
	req.Header.Set("tracestate", "vendor=1")

	handleEgress(&http.Client{Timeout: 5 * time.Second}, dep.URL, http.MethodGet)(rec, req)

	if got.Get("traceparent") != tp {
		t.Errorf("outbound traceparent = %q, want %q", got.Get("traceparent"), tp)
	}
	if got.Get("tracestate") != "vendor=1" {
		t.Errorf("outbound tracestate = %q, want it propagated", got.Get("tracestate"))
	}

	var out egressResponse
	if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Calls) != 1 || out.Calls[0].Status != 200 {
		t.Fatalf("calls = %+v", out.Calls)
	}
	if out.Calls[0].Error != "" {
		t.Errorf("call error: %s", out.Calls[0].Error)
	}
}

// The query string is part of the mock key Envoy later looks up, so the app
// must pass it through to the dependency untouched.
func TestOutboundCallPreservesQueryString(t *testing.T) {
	var gotURI string
	dep := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotURI = r.URL.RequestURI()
	}))
	defer dep.Close()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/egress/get?path=/dep/echo%3Factive=true", nil)
	handleEgress(&http.Client{Timeout: 5 * time.Second}, dep.URL, http.MethodGet)(rec, req)

	if gotURI != "/dep/echo?active=true" {
		t.Errorf("dependency saw %q, want the query preserved", gotURI)
	}
}

// Distinct dependency calls must stay distinct, or several egress mocks
// collapse onto one key and the wrong response gets replayed.
func TestMultiCallProducesDistinctURLs(t *testing.T) {
	seen := map[string]bool{}
	dep := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen[r.URL.RequestURI()] = true
	}))
	defer dep.Close()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/egress/multi?n=3", nil)
	handleEgressMulti(&http.Client{Timeout: 5 * time.Second}, dep.URL)(rec, req)

	if len(seen) != 3 {
		t.Fatalf("dependency saw %d distinct URIs, want 3: %v", len(seen), seen)
	}
}

// The dependency must emit exactly the requested byte count so a large-body
// mock can be verified end to end.
func TestDepSizeReturnsExactBytes(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/dep/size/5000", nil)
	req.SetPathValue("n", "5000")
	handleDepSize(rec, req)

	if got := rec.Body.Len(); got != 5000 {
		t.Fatalf("body = %d bytes, want 5000", got)
	}
	if strings.Trim(rec.Body.String(), "z") != "" {
		t.Error("body should be all 'z' filler")
	}
}
