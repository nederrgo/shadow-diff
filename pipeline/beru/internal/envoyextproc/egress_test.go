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

func TestParseRecordAndReplayConfigs_monarchJSON(t *testing.T) {
	raw := `[{"host":"httpbin.org","ignoreRequestPaths":["$.nonce"]}]`
	configs := parseRecordAndReplayConfigs(raw)
	if len(configs) != 1 {
		t.Fatalf("configs: %v", configs)
	}
	if configs[0].Host != "httpbin.org" || len(configs[0].IgnoreRequestPaths) != 1 {
		t.Fatalf("unexpected: %+v", configs[0])
	}
}

func TestIgnorePathsForHost(t *testing.T) {
	configs := []RecordAndReplayHostConfig{
		{Host: "api.example.com", IgnoreRequestPaths: []string{"$.timestamp"}},
	}
	paths := ignorePathsForHost(configs, "api.example.com")
	if len(paths) != 1 || paths[0] != "$.timestamp" {
		t.Fatalf("unexpected paths: %v", paths)
	}
}

// TestHandleEgressRequest_ignorePathsVariedBody verifies cache lookup strips ignored
// JSON fields so requests differing only in those fields share the same mock hash.
func TestHandleEgressRequest_ignorePathsVariedBody(t *testing.T) {
	mocks := replay.NewMockStore()
	s := &Server{Log: slog.Default(), Mocks: mocks}

	host := "httpbin.org"
	seedBody := []byte(`{"foo":1,"nonce":"seed-value"}`)
	hash, err := replay.HashRequest("POST", host, "/post", seedBody, []string{"$.nonce"})
	if err != nil {
		t.Fatal(err)
	}
	mocks.Put(hash, replay.EarlyResponse{
		StatusCode: 200,
		Headers:    map[string]string{"content-type": "application/json"},
		Body:       []byte(`{"mock":true}`),
	})

	configs := []RecordAndReplayHostConfig{
		{Host: host, IgnoreRequestPaths: []string{"$.nonce"}},
	}
	state := &egressState{role: "control-a", recordAndReplayConfigs: configs}
	s.handleEgressRequest(state, &extprocv3.ProcessingRequest{
		Request: &extprocv3.ProcessingRequest_RequestHeaders{
			RequestHeaders: &extprocv3.HttpHeaders{
				Headers: &corev3.HeaderMap{Headers: []*corev3.HeaderValue{
					{Key: ":method", RawValue: []byte("POST")},
					{Key: ":authority", RawValue: []byte(host)},
					{Key: ":path", RawValue: []byte("/post")},
				}},
			},
		},
	})

	variedBody := []byte(`{"foo":1,"nonce":"different-value"}`)
	resp := s.handleEgressRequest(state, &extprocv3.ProcessingRequest{
		Request: &extprocv3.ProcessingRequest_RequestBody{
			RequestBody: &extprocv3.HttpBody{Body: variedBody, EndOfStream: true},
		},
	})
	imm := resp.GetImmediateResponse()
	if imm == nil {
		t.Fatal("expected immediate response when ignored fields differ")
	}
	if imm.GetStatus().GetCode() != 200 {
		t.Fatalf("expected mock hit 200, got %v (ignore paths may not be applied)", imm.GetStatus().GetCode())
	}
	if string(imm.GetBody()) != `{"mock":true}` {
		t.Fatalf("unexpected body: %s", imm.GetBody())
	}

	// Without ignore paths the varied body must miss.
	missState := &egressState{role: "control-a", recordAndReplayConfigs: nil}
	s.handleEgressRequest(missState, &extprocv3.ProcessingRequest{
		Request: &extprocv3.ProcessingRequest_RequestHeaders{
			RequestHeaders: &extprocv3.HttpHeaders{
				Headers: &corev3.HeaderMap{Headers: []*corev3.HeaderValue{
					{Key: ":method", RawValue: []byte("POST")},
					{Key: ":authority", RawValue: []byte(host)},
					{Key: ":path", RawValue: []byte("/post")},
				}},
			},
		},
	})
	missResp := s.handleEgressRequest(missState, &extprocv3.ProcessingRequest{
		Request: &extprocv3.ProcessingRequest_RequestBody{
			RequestBody: &extprocv3.HttpBody{Body: variedBody, EndOfStream: true},
		},
	})
	missImm := missResp.GetImmediateResponse()
	if missImm == nil || int(missImm.GetStatus().GetCode()) != egressMissStatus {
		t.Fatalf("expected 599 without ignore paths, got %+v", missImm)
	}
}
