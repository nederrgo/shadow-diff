package envoyextproc

import (
	"log/slog"

	corev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	extprocv3 "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"
	typev3 "github.com/envoyproxy/go-control-plane/envoy/type/v3"
	"github.com/shadow-diff/shop/internal/replay"
	"github.com/shadow-diff/shop/internal/trace"
)

const (
	egressRegressionBody = "Egress Regression"
	egressMissStatus     = 599
)

type egressState struct {
	traceID string
	method  string
	host    string
	path    string
	body    []byte
}

func (s *Server) handleEgressRequest(state *egressState, req *extprocv3.ProcessingRequest) *extprocv3.ProcessingResponse {
	switch v := req.Request.(type) {
	case *extprocv3.ProcessingRequest_RequestHeaders:
		s.captureEgressRequestHeaders(state, v.RequestHeaders)
		// Trace ID and host are in headers; body is not needed for mock lookup.
		// request_body_mode=NONE means the body case never fires anyway.
		return s.egressImmediateFromState(state)
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
	headers := hdrs.GetHeaders()
	state.traceID = trace.TraceIDFromMap(headers, headerValue)
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
	if state.traceID == "" {
		slog.Info("Egress Regression: no trace ID", "method", state.method, "host", state.host, "path", state.path)
		return immediateResponse(egressMissStatus, nil, []byte(egressRegressionBody), "egress no trace id")
	}

	hostKey := replay.HostWithoutPort(state.host)
	key := replay.TraceKey(state.traceID, state.method, hostKey, state.path)
	if mock, ok := s.Mocks.Get(key); ok {
		return immediateResponse(mock.StatusCode, mock.Headers, mock.Body, "egress mock hit")
	}

	slog.Info("Egress Regression", "trace_id", state.traceID, "method", state.method, "host", hostKey, "path", state.path)
	return immediateResponse(egressMissStatus, nil, []byte(egressRegressionBody), "egress regression")
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
