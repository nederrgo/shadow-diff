package envoyextproc

import (
	"context"
	"log/slog"

	corev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	extprocv3 "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"
	typev3 "github.com/envoyproxy/go-control-plane/envoy/type/v3"
	"github.com/shadow-diff/beruclient"
	"github.com/shadow-diff/shop/internal/beru"
	"github.com/shadow-diff/shop/internal/replay"
	"github.com/shadow-diff/trace"
)

const (
	egressRegressionBody = "Egress Regression"
	egressMissStatus     = 599
)

type egressState struct {
	role    string
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
		// Wait for BUFFERED body (may be empty). ImmediateResponse ends the stream,
		// so lookup + Beru report happen on end_of_stream only.
		if v.RequestHeaders != nil && v.RequestHeaders.GetEndOfStream() {
			return s.finishEgress(state)
		}
		return requestHeaderContinueResponse()
	case *extprocv3.ProcessingRequest_RequestBody:
		if v.RequestBody != nil {
			state.body = append(state.body, v.RequestBody.GetBody()...)
		}
		if v.RequestBody == nil || v.RequestBody.GetEndOfStream() {
			return s.finishEgress(state)
		}
		return requestBodyContinueResponse()
	default:
		return requestHeaderContinueResponse()
	}
}

func (s *Server) finishEgress(state *egressState) *extprocv3.ProcessingResponse {
	resp, status := s.egressImmediateFromState(state)
	s.maybeReportEgress(state, status)
	return resp
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

func (s *Server) egressImmediateFromState(state *egressState) (*extprocv3.ProcessingResponse, int) {
	if s.Mocks == nil {
		return immediateResponse(egressMissStatus, nil, []byte(egressRegressionBody), "egress mock store unavailable"), egressMissStatus
	}
	if state.traceID == "" {
		slog.Info("Egress Regression: no trace ID", "method", state.method, "host", state.host, "path", state.path)
		return immediateResponse(egressMissStatus, nil, []byte(egressRegressionBody), "egress no trace id"), egressMissStatus
	}

	hostKey := replay.HostWithoutPort(state.host)
	key := replay.TraceKey(state.traceID, state.method, hostKey, state.path)
	if mock, ok := s.Mocks.Get(key); ok {
		return immediateResponse(mock.StatusCode, mock.Headers, mock.Body, "egress mock hit"), mock.StatusCode
	}

	slog.Info("Egress Regression", "trace_id", state.traceID, "method", state.method, "host", hostKey, "path", state.path)
	return immediateResponse(egressMissStatus, nil, []byte(egressRegressionBody), "egress regression"), egressMissStatus
}

func (s *Server) maybeReportEgress(state *egressState, status int) {
	if s.Beru == nil || state.traceID == "" || state.role == "" {
		return
	}
	hostKey := replay.HostWithoutPort(state.host)
	bodyCopy := append([]byte(nil), state.body...)
	payload, err := beru.BuildHTTPEgressPayload(state.method, hostKey, state.path, status, bodyCopy)
	if err != nil {
		slog.Warn("shop beru report: marshal payload", "err", err)
		return
	}
	report := beruclient.Report{
		TraceID:        state.traceID,
		Workload:       state.role,
		Protocol:       "http",
		Payload:        payload,
		ShadowTestName: s.ShadowTestName,
	}

	traceID := report.TraceID
	role := report.Workload
	go func() {
		if err := s.Beru.PostReport(context.Background(), report); err != nil {
			slog.Warn("shop beru report failed", "trace_id", traceID, "role", role, "err", err)
		}
	}()
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
