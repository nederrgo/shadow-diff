package envoyextproc

import (
	"io"
	"log/slog"
	"os"
	"strings"

	corev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	extprocv3 "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"
	"github.com/shadow-diff/beru/internal/replay"
	"github.com/shadow-diff/beru/internal/trace"
	v2engine "github.com/shadow-diff/beru/internal/v2/engine"
	v2report "github.com/shadow-diff/beru/internal/v2/report"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

const (
	headerShadowTraceID = "x-shadow-trace-id"
	headerShadowRole    = "x-shadow-role"
	headerRequestID     = "x-request-id"
)

// Server implements Envoy external processing (observe-only, non-blocking).
type Server struct {
	extprocv3.UnimplementedExternalProcessorServer
	Log               *slog.Logger
	Router            *v2engine.TraceRouter
	Mocks             *replay.MockStore
	Role              string
	DefaultShadowTest string
}

type streamState struct {
	traceID         string
	role            string
	shadowTestName  string
	method          string
	path            string
	responseMeta    map[string]string
	responseStatus  string
	contentType     string
}

// Process handles the ext_proc bidirectional stream.
func (s *Server) Process(stream extprocv3.ExternalProcessor_ProcessServer) error {
	role := s.Role
	mode := ""
	var recordAndReplayConfigs []RecordAndReplayHostConfig
	if md, ok := metadata.FromIncomingContext(stream.Context()); ok {
		if v := md.Get(headerShadowRole); len(v) > 0 && v[0] != "" {
			role = v[0]
		}
		if v := md.Get(headerShadowMode); len(v) > 0 {
			mode = v[0]
		}
		if v := md.Get(headerShadowRecordAndReplayConf); len(v) > 0 && v[0] != "" {
			recordAndReplayConfigs = parseRecordAndReplayConfigs(v[0])
		}
	}

	if mode == shadowModeEgress {
		egress := &egressState{role: role, recordAndReplayConfigs: recordAndReplayConfigs}
		for {
			req, err := stream.Recv()
			if err == io.EOF {
				return nil
			}
			if err != nil {
				return status.Errorf(codes.Unknown, "recv: %v", err)
			}
			resp := s.handleEgressRequest(egress, req)
			if err := stream.Send(resp); err != nil {
				return status.Errorf(codes.Unknown, "send: %v", err)
			}
		}
	}

	state := &streamState{role: role, responseMeta: map[string]string{}}
	for {
		req, err := stream.Recv()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return status.Errorf(codes.Unknown, "recv: %v", err)
		}
		resp := s.handleRequest(state, req)
		if err := stream.Send(resp); err != nil {
			return status.Errorf(codes.Unknown, "send: %v", err)
		}
	}
}

func (s *Server) handleRequest(state *streamState, req *extprocv3.ProcessingRequest) *extprocv3.ProcessingResponse {
	switch v := req.Request.(type) {
	case *extprocv3.ProcessingRequest_RequestHeaders:
		s.captureRequestHeaders(state, v.RequestHeaders.GetHeaders())
		return requestHeaderContinueResponse()
	case *extprocv3.ProcessingRequest_ResponseHeaders:
		s.captureResponseHeaders(state, v.ResponseHeaders.GetHeaders())
		return responseHeaderContinueResponse()
	case *extprocv3.ProcessingRequest_ResponseBody:
		s.ingestResponseBody(state, v.ResponseBody)
		return responseBodyContinueResponse()
	default:
		return requestHeaderContinueResponse()
	}
}

func (s *Server) captureRequestHeaders(state *streamState, headers *corev3.HeaderMap) {
	if headers == nil {
		return
	}
	state.traceID = trace.ShadowTraceIDFromMap(headers, headerValue)
	state.method = headerValue(headers, ":method")
	state.path = headerValue(headers, ":path")
	if r := headerValue(headers, headerShadowRole); r != "" {
		state.role = r
	} else if state.role == "" {
		state.role = s.Role
	}
	if n := headerValue(headers, "x-shadow-test-name"); n != "" {
		state.shadowTestName = n
	} else if s.DefaultShadowTest != "" {
		state.shadowTestName = s.DefaultShadowTest
	}
}

func (s *Server) captureResponseHeaders(state *streamState, headers *corev3.HeaderMap) {
	if headers == nil {
		return
	}
	state.responseStatus = headerValue(headers, ":status")
	state.contentType = headerValue(headers, "content-type")
	for _, h := range headers.Headers {
		k := strings.ToLower(h.Key)
		state.responseMeta[k] = headerValueFrom(h)
	}
}

func (s *Server) ingestResponseBody(state *streamState, body *extprocv3.HttpBody) {
	if body == nil || state.traceID == "" || state.role == "" {
		return
	}
	data := body.GetBody()
	if len(data) == 0 {
		return
	}
	meta := map[string]string{}
	for k, v := range state.responseMeta {
		meta[k] = v
	}
	if state.responseStatus != "" {
		meta[":status"] = state.responseStatus
	}
	if s.Router != nil {
		if raw, err := v2report.FromHTTPIngress(
			state.traceID, state.role, state.shadowTestName, state.method, state.path,
			meta, data, state.contentType,
		); err == nil {
			s.Router.Route(raw)
		}
	}
}

func headerValue(headers *corev3.HeaderMap, key string) string {
	if headers == nil {
		return ""
	}
	kl := strings.ToLower(key)
	for _, h := range headers.Headers {
		if strings.ToLower(h.Key) == kl {
			return headerValueFrom(h)
		}
	}
	return ""
}

func headerValueFrom(h *corev3.HeaderValue) string {
	if h == nil {
		return ""
	}
	if len(h.RawValue) > 0 {
		return string(h.RawValue)
	}
	return h.Value
}

func continueCommon() *extprocv3.CommonResponse {
	return &extprocv3.CommonResponse{
		Status: extprocv3.CommonResponse_CONTINUE,
	}
}

func requestHeaderContinueResponse() *extprocv3.ProcessingResponse {
	return &extprocv3.ProcessingResponse{
		Response: &extprocv3.ProcessingResponse_RequestHeaders{
			RequestHeaders: &extprocv3.HeadersResponse{Response: continueCommon()},
		},
	}
}

func responseHeaderContinueResponse() *extprocv3.ProcessingResponse {
	return &extprocv3.ProcessingResponse{
		Response: &extprocv3.ProcessingResponse_ResponseHeaders{
			ResponseHeaders: &extprocv3.HeadersResponse{Response: continueCommon()},
		},
	}
}

func responseBodyContinueResponse() *extprocv3.ProcessingResponse {
	return &extprocv3.ProcessingResponse{
		Response: &extprocv3.ProcessingResponse_ResponseBody{
			ResponseBody: &extprocv3.BodyResponse{Response: continueCommon()},
		},
	}
}

// RoleFromEnv reads SHADOW_ROLE for this sidecar instance.
func RoleFromEnv() string {
	return os.Getenv("SHADOW_ROLE")
}
