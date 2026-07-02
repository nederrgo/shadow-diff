package envoyextproc

import (
	"log/slog"
	"testing"

	corev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	extprocv3 "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"
	"github.com/shadow-diff/beru/internal/replay"
)

const testTraceID = "4bf92f3577b34da6a3ce929d0e0e4736"

func makeEgressRequest(method, authority, path, traceparent string, body []byte, eos bool) []*extprocv3.ProcessingRequest {
	hdrs := []*corev3.HeaderValue{
		{Key: ":method", RawValue: []byte(method)},
		{Key: ":authority", RawValue: []byte(authority)},
		{Key: ":path", RawValue: []byte(path)},
	}
	if traceparent != "" {
		hdrs = append(hdrs, &corev3.HeaderValue{Key: "traceparent", RawValue: []byte(traceparent)})
	}
	reqs := []*extprocv3.ProcessingRequest{
		{
			Request: &extprocv3.ProcessingRequest_RequestHeaders{
				RequestHeaders: &extprocv3.HttpHeaders{
					Headers:     &corev3.HeaderMap{Headers: hdrs},
					EndOfStream: body == nil,
				},
			},
		},
	}
	if body != nil {
		reqs = append(reqs, &extprocv3.ProcessingRequest{
			Request: &extprocv3.ProcessingRequest_RequestBody{
				RequestBody: &extprocv3.HttpBody{Body: body, EndOfStream: eos},
			},
		})
	}
	return reqs
}

func runEgress(s *Server, reqs []*extprocv3.ProcessingRequest) *extprocv3.ProcessingResponse {
	state := &egressState{role: "control-a"}
	var last *extprocv3.ProcessingResponse
	for _, r := range reqs {
		last = s.handleEgressRequest(state, r)
	}
	return last
}

func seedMock(mocks *replay.MockStore, traceID, method, host, path string) {
	key := replay.TraceKey(traceID, method, host, path)
	mocks.Put(key, replay.EarlyResponse{
		StatusCode: 200,
		Headers:    map[string]string{"content-type": "application/json"},
		Body:       []byte(`{"mock":true}`),
	})
}

// TestHandleEgressRequest_traceIDHit verifies that the trace-ID-keyed mock is returned
// when traceparent carries the matching trace ID, regardless of request body content.
func TestHandleEgressRequest_traceIDHit(t *testing.T) {
	mocks := replay.NewMockStore()
	s := &Server{Log: slog.Default(), Mocks: mocks}

	seedMock(mocks, testTraceID, "POST", "api.example.com", "/v1/orders")

	tp := "00-" + testTraceID + "-00f067aa0ba902b7-01"
	reqs := makeEgressRequest("POST", "api.example.com", "/v1/orders", tp, []byte(`{"amount":100}`), true)
	resp := runEgress(s, reqs)

	imm := resp.GetImmediateResponse()
	if imm == nil {
		t.Fatal("expected immediate response on trace-ID hit")
	}
	if imm.GetStatus().GetCode() != 200 {
		t.Fatalf("expected 200, got %v", imm.GetStatus().GetCode())
	}
	if string(imm.GetBody()) != `{"mock":true}` {
		t.Fatalf("unexpected body: %s", imm.GetBody())
	}
}

// TestHandleEgressRequest_bodyDivergentHit is the key assertion for the new architecture:
// same trace ID + different body → still gets the production record.
func TestHandleEgressRequest_bodyDivergentHit(t *testing.T) {
	mocks := replay.NewMockStore()
	s := &Server{Log: slog.Default(), Mocks: mocks}

	seedMock(mocks, testTraceID, "POST", "api.example.com", "/v1/orders")

	tp := "00-" + testTraceID + "-00f067aa0ba902b7-01"
	// Candidate sends a mutated body — completely different from what was recorded.
	differentBody := []byte(`{"amount":999,"extra_field":"candidate-added-this"}`)
	reqs := makeEgressRequest("POST", "api.example.com", "/v1/orders", tp, differentBody, true)
	resp := runEgress(s, reqs)

	imm := resp.GetImmediateResponse()
	if imm == nil {
		t.Fatal("expected immediate response despite body difference")
	}
	if imm.GetStatus().GetCode() != 200 {
		t.Fatalf("body-divergent candidate should get 200 via trace ID, got %v", imm.GetStatus().GetCode())
	}
}

// TestHandleEgressRequest_noTraceID verifies that requests missing traceparent get 599.
func TestHandleEgressRequest_noTraceID(t *testing.T) {
	mocks := replay.NewMockStore()
	s := &Server{Log: slog.Default(), Mocks: mocks}

	seedMock(mocks, testTraceID, "POST", "api.example.com", "/v1/orders")

	// No traceparent header.
	reqs := makeEgressRequest("POST", "api.example.com", "/v1/orders", "", []byte(`{"amount":100}`), true)
	resp := runEgress(s, reqs)

	imm := resp.GetImmediateResponse()
	if imm == nil || int(imm.GetStatus().GetCode()) != egressMissStatus {
		t.Fatalf("expected 599 when no trace ID, got %+v", imm)
	}
}

// TestHandleEgressRequest_unknownTraceID verifies that an unknown trace ID gives 599.
func TestHandleEgressRequest_unknownTraceID(t *testing.T) {
	mocks := replay.NewMockStore()
	s := &Server{Log: slog.Default(), Mocks: mocks}

	seedMock(mocks, testTraceID, "POST", "api.example.com", "/v1/orders")

	differentTrace := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaabb"
	tp := "00-" + differentTrace + "-00f067aa0ba902b7-01"
	reqs := makeEgressRequest("POST", "api.example.com", "/v1/orders", tp, []byte(`{"amount":100}`), true)
	resp := runEgress(s, reqs)

	imm := resp.GetImmediateResponse()
	if imm == nil || int(imm.GetStatus().GetCode()) != egressMissStatus {
		t.Fatalf("expected 599 for unknown trace ID, got %+v", imm)
	}
}

// TestHandleEgressRequest_nilMocks verifies graceful handling when mock store is absent.
func TestHandleEgressRequest_nilMocks(t *testing.T) {
	s := &Server{Log: slog.Default(), Mocks: nil}

	tp := "00-" + testTraceID + "-00f067aa0ba902b7-01"
	reqs := makeEgressRequest("GET", "api.example.com", "/", tp, nil, true)
	resp := runEgress(s, reqs)

	imm := resp.GetImmediateResponse()
	if imm == nil || int(imm.GetStatus().GetCode()) != egressMissStatus {
		t.Fatalf("expected 599 with nil mocks, got %+v", imm)
	}
}

func TestHostWithoutPort(t *testing.T) {
	if got := hostWithoutPort("api.example.com:443"); got != "api.example.com" {
		t.Fatalf("got %q", got)
	}
	if got := hostWithoutPort("api.example.com"); got != "api.example.com" {
		t.Fatalf("got %q", got)
	}
}
