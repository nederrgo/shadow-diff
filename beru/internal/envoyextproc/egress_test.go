package envoyextproc

import (
	"log/slog"
	"testing"

	corev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	extprocv3 "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"
	"github.com/shadow-diff/beru/internal/replay"
)

func TestHandleEgressRequest_mockHitAndMiss(t *testing.T) {
	mocks := replay.NewMockStore()
	s := &Server{Log: slog.Default(), Mocks: mocks}

	body := []byte(`{"amount":100}`)
	hash, err := replay.HashRequest("POST", "api.example.com", "/v1/orders", body, nil)
	if err != nil {
		t.Fatal(err)
	}
	mocks.Put(hash, replay.EarlyResponse{
		StatusCode: 200,
		Headers:    map[string]string{"content-type": "application/json"},
		Body:       []byte(`{"ok":true}`),
	})

	state := &egressState{role: "control-a"}
	hdrResp := s.handleEgressRequest(state, &extprocv3.ProcessingRequest{
		Request: &extprocv3.ProcessingRequest_RequestHeaders{
			RequestHeaders: &extprocv3.HttpHeaders{
				Headers: &corev3.HeaderMap{Headers: []*corev3.HeaderValue{
					{Key: ":method", RawValue: []byte("POST")},
					{Key: ":authority", RawValue: []byte("api.example.com")},
					{Key: ":path", RawValue: []byte("/v1/orders")},
				}},
			},
		},
	})
	if hdrResp.GetRequestHeaders() == nil {
		t.Fatal("expected continue on request headers")
	}

	bodyResp := s.handleEgressRequest(state, &extprocv3.ProcessingRequest{
		Request: &extprocv3.ProcessingRequest_RequestBody{
			RequestBody: &extprocv3.HttpBody{Body: body, EndOfStream: true},
		},
	})
	imm := bodyResp.GetImmediateResponse()
	if imm == nil {
		t.Fatal("expected immediate response on mock hit")
	}
	if imm.GetStatus().GetCode() != 200 {
		t.Fatalf("expected 200, got %v", imm.GetStatus().GetCode())
	}

	missState := &egressState{role: "control-a"}
	s.handleEgressRequest(missState, &extprocv3.ProcessingRequest{
		Request: &extprocv3.ProcessingRequest_RequestHeaders{
			RequestHeaders: &extprocv3.HttpHeaders{
				Headers: &corev3.HeaderMap{Headers: []*corev3.HeaderValue{
					{Key: ":method", RawValue: []byte("GET")},
					{Key: ":authority", RawValue: []byte("unknown.example.com")},
					{Key: ":path", RawValue: []byte("/missing")},
				}},
			},
		},
	})
	missResp := s.handleEgressRequest(missState, &extprocv3.ProcessingRequest{
		Request: &extprocv3.ProcessingRequest_RequestBody{
			RequestBody: &extprocv3.HttpBody{EndOfStream: true},
		},
	})
	missImm := missResp.GetImmediateResponse()
	if missImm == nil || int(missImm.GetStatus().GetCode()) != egressMissStatus {
		t.Fatalf("expected 599 on miss, got %+v", missImm)
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

func TestIgnorePathsForHost(t *testing.T) {
	configs := []DownstreamConfig{
		{Host: "api.example.com", IgnoreRequestPaths: []string{"$.timestamp"}},
	}
	paths := ignorePathsForHost(configs, "api.example.com")
	if len(paths) != 1 || paths[0] != "$.timestamp" {
		t.Fatalf("unexpected paths: %v", paths)
	}
}
