package envoyextproc

import (
	"encoding/json"
	"net"
	"strings"

	corev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	extprocv3 "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"
	typev3 "github.com/envoyproxy/go-control-plane/envoy/type/v3"
	"github.com/shadow-diff/beru/internal/replay"
)

const (
	headerShadowMode            = "x-shadow-mode"
	headerShadowDownstreamsConf = "x-shadow-downstreams-config"
	shadowModeEgress            = "egress"
	egressRegressionBody        = "Egress Regression"
	egressMissStatus            = 599
)

// DownstreamConfig mirrors Monarch DownstreamSpec for ext_proc metadata.
type DownstreamConfig struct {
	Host               string   `json:"host"`
	IgnoreRequestPaths []string `json:"ignoreRequestPaths"`
}

type egressState struct {
	role               string
	method             string
	host               string
	path               string
	body               []byte
	downstreamConfigs  []DownstreamConfig
	endOfStreamHeaders bool
}

func (s *Server) handleEgressRequest(state *egressState, req *extprocv3.ProcessingRequest) *extprocv3.ProcessingResponse {
	switch v := req.Request.(type) {
	case *extprocv3.ProcessingRequest_RequestHeaders:
		s.captureEgressRequestHeaders(state, v.RequestHeaders)
		if state.endOfStreamHeaders {
			return s.egressImmediateFromState(state)
		}
		return requestHeaderContinueResponse()
	case *extprocv3.ProcessingRequest_RequestBody:
		if v.RequestBody != nil {
			state.body = append(state.body, v.RequestBody.GetBody()...)
		}
		if v.RequestBody == nil || v.RequestBody.GetEndOfStream() {
			return s.egressImmediateFromState(state)
		}
		return requestBodyContinueResponse()
	default:
		return requestHeaderContinueResponse()
	}
}

func (s *Server) captureEgressRequestHeaders(state *egressState, hdrs *extprocv3.HttpHeaders) {
	if hdrs == nil {
		return
	}
	state.endOfStreamHeaders = hdrs.GetEndOfStream()
	headers := hdrs.GetHeaders()
	state.method = headerValue(headers, ":method")
	state.host = headerValue(headers, ":authority")
	if state.host == "" {
		state.host = headerValue(headers, "host")
	}
	state.path = headerValue(headers, ":path")
}

func (s *Server) egressImmediateFromState(state *egressState) *extprocv3.ProcessingResponse {
	if s.Mocks == nil {
		return immediateResponse(egressMissStatus, nil, []byte(egressRegressionBody), "egress mock store unavailable")
	}

	hostKey := hostWithoutPort(state.host)
	ignorePaths := ignorePathsForHost(state.downstreamConfigs, hostKey)

	hash, err := replay.HashRequest(state.method, hostKey, state.path, state.body, ignorePaths)
	if err != nil {
		s.Log.Warn("Could not hash egress request", "err", err, "host", hostKey, "path", state.path)
		return immediateResponse(egressMissStatus, nil, []byte(egressRegressionBody), "egress hash error")
	}

	if mock, ok := s.Mocks.Get(hash); ok {
		return immediateResponse(mock.StatusCode, mock.Headers, mock.Body, "egress mock hit")
	}

	s.Log.Info("Egress Regression", "hash", hash, "method", state.method, "host", hostKey, "path", state.path)
	return immediateResponse(egressMissStatus, nil, []byte(egressRegressionBody), "egress regression")
}

func hostWithoutPort(host string) string {
	if host == "" {
		return ""
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		return h
	}
	return host
}

func ignorePathsForHost(configs []DownstreamConfig, host string) []string {
	host = strings.ToLower(host)
	for _, c := range configs {
		if strings.EqualFold(c.Host, host) {
			return c.IgnoreRequestPaths
		}
	}
	return nil
}

func parseDownstreamConfigs(raw string) []DownstreamConfig {
	if raw == "" {
		return nil
	}
	var configs []DownstreamConfig
	if err := json.Unmarshal([]byte(raw), &configs); err != nil {
		return nil
	}
	return configs
}

func immediateResponse(statusCode int, headers map[string]string, body []byte, details string) *extprocv3.ProcessingResponse {
	hdrs := []*corev3.HeaderValueOption{}
	for k, v := range headers {
		hdrs = append(hdrs, &corev3.HeaderValueOption{
			Header: &corev3.HeaderValue{Key: k, RawValue: []byte(v)},
		})
	}
	return &extprocv3.ProcessingResponse{
		Response: &extprocv3.ProcessingResponse_ImmediateResponse{
			ImmediateResponse: &extprocv3.ImmediateResponse{
				Status: &typev3.HttpStatus{Code: typev3.StatusCode(statusCode)},
				Headers: &extprocv3.HeaderMutation{
					SetHeaders: hdrs,
				},
				Body: body,
				GrpcStatus: &extprocv3.GrpcStatus{
					Status: 0,
				},
				Details: details,
			},
		},
	}
}

func requestBodyContinueResponse() *extprocv3.ProcessingResponse {
	return &extprocv3.ProcessingResponse{
		Response: &extprocv3.ProcessingResponse_RequestBody{
			RequestBody: &extprocv3.BodyResponse{Response: continueCommon()},
		},
	}
}
