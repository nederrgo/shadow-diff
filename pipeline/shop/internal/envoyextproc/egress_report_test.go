package envoyextproc

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	corev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	extprocv3 "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"
	"github.com/shadow-diff/shop/internal/beru"
	"github.com/shadow-diff/shop/internal/replay"
)

const testTraceID = "4bf92f3577b34da6a3ce929d0e0e4736"

func TestFinishEgress_reportsBodyAsync(t *testing.T) {
	var mu sync.Mutex
	var got beru.Report
	done := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		mu.Lock()
		_ = json.Unmarshal(raw, &got)
		mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{}`))
		close(done)
	}))
	defer srv.Close()

	mocks := replay.NewMockStore()
	mocks.Put(replay.TraceKey(testTraceID, "POST", "billing.internal", "/v1/charges"), replay.EarlyResponse{
		StatusCode: 201,
		Body:       []byte(`{"ok":true}`),
	})

	s := &Server{
		Mocks:          mocks,
		Beru:           beru.NewClient(srv.URL),
		ShadowTestName: "my-test",
	}
	state := &egressState{
		role:    "candidate",
		traceID: testTraceID,
		method:  "POST",
		host:    "billing.internal:443",
		path:    "/v1/charges",
		body:    []byte(`{"amount":100}`),
	}
	resp := s.finishEgress(state)
	imm := resp.GetImmediateResponse()
	if imm == nil || imm.Status.GetCode() != 201 {
		t.Fatalf("expected ImmediateResponse 201, got %#v", resp)
	}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for Beru report")
	}

	mu.Lock()
	defer mu.Unlock()
	if got.TraceID != testTraceID || got.Workload != "candidate" || got.Protocol != "http" {
		t.Fatalf("report envelope: %+v", got)
	}
	if got.ShadowTestName != "my-test" {
		t.Fatalf("shadow_test_name = %q", got.ShadowTestName)
	}
	var payload map[string]any
	if err := json.Unmarshal(got.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["method"] != "POST" || payload["host"] != "billing.internal" || payload["path"] != "/v1/charges" {
		t.Fatalf("payload: %v", payload)
	}
	if int(payload["status"].(float64)) != 201 {
		t.Fatalf("status: %v", payload["status"])
	}
	body, ok := payload["body"].(map[string]any)
	if !ok || int(body["amount"].(float64)) != 100 {
		t.Fatalf("body: %v", payload["body"])
	}
}

func TestFinishEgress_getEmptyBody(t *testing.T) {
	var mu sync.Mutex
	var got beru.Report
	done := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		mu.Lock()
		_ = json.Unmarshal(raw, &got)
		mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
		close(done)
	}))
	defer srv.Close()

	mocks := replay.NewMockStore()
	mocks.Put(replay.TraceKey(testTraceID, "GET", "api.example.com", "/v1/orders"), replay.EarlyResponse{
		StatusCode: 200,
		Body:       []byte(`[]`),
	})
	s := &Server{Mocks: mocks, Beru: beru.NewClient(srv.URL)}
	resp := s.finishEgress(&egressState{
		role:    "control-a",
		traceID: testTraceID,
		method:  "GET",
		host:    "api.example.com",
		path:    "/v1/orders",
	})
	if resp.GetImmediateResponse().Status.GetCode() != 200 {
		t.Fatalf("status = %v", resp.GetImmediateResponse().Status)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for Beru report")
	}
	mu.Lock()
	defer mu.Unlock()
	var payload map[string]any
	_ = json.Unmarshal(got.Payload, &payload)
	if payload["body"] != "" {
		t.Fatalf("expected empty body string, got %#v", payload["body"])
	}
}

func TestHandleEgressRequest_waitsForBody(t *testing.T) {
	mocks := replay.NewMockStore()
	mocks.Put(replay.TraceKey(testTraceID, "POST", "h", "/p"), replay.EarlyResponse{StatusCode: 200})
	s := &Server{Mocks: mocks}
	state := &egressState{role: "control-a"}

	hdrResp := s.handleEgressRequest(state, &extprocv3.ProcessingRequest{
		Request: &extprocv3.ProcessingRequest_RequestHeaders{
			RequestHeaders: &extprocv3.HttpHeaders{
				Headers: &corev3.HeaderMap{Headers: []*corev3.HeaderValue{
					{Key: ":method", RawValue: []byte("POST")},
					{Key: ":authority", RawValue: []byte("h")},
					{Key: ":path", RawValue: []byte("/p")},
					{Key: "traceparent", RawValue: []byte("00-" + testTraceID + "-00f067aa0ba902b7-01")},
				}},
			},
		},
	})
	if hdrResp.GetRequestHeaders() == nil {
		t.Fatal("expected Continue on headers, not ImmediateResponse")
	}

	final := s.handleEgressRequest(state, &extprocv3.ProcessingRequest{
		Request: &extprocv3.ProcessingRequest_RequestBody{
			RequestBody: &extprocv3.HttpBody{Body: []byte(`{"x":1}`), EndOfStream: true},
		},
	})
	if final.GetImmediateResponse() == nil {
		t.Fatal("expected ImmediateResponse after body EOS")
	}
	if string(state.body) != `{"x":1}` {
		t.Fatalf("body = %q", state.body)
	}
}

