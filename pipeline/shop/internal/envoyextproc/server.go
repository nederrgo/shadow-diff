package envoyextproc

import (
	"io"
	"strings"

	corev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	extprocv3 "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"
	"github.com/shadow-diff/shop/internal/beru"
	"github.com/shadow-diff/shop/internal/replay"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

const headerShadowRole = "x-shadow-role"

// Server implements Envoy external processing for egress mock lookup only.
type Server struct {
	extprocv3.UnimplementedExternalProcessorServer
	Mocks          *replay.MockStore
	Beru           *beru.Client
	ShadowTestName string
}

// Process handles the ext_proc bidirectional stream (always egress mode).
func (s *Server) Process(stream extprocv3.ExternalProcessor_ProcessServer) error {
	role := ""
	if md, ok := metadata.FromIncomingContext(stream.Context()); ok {
		if v := md.Get(headerShadowRole); len(v) > 0 && v[0] != "" {
			role = v[0]
		}
	}
	egress := &egressState{role: role}
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

func headerValue(headers *corev3.HeaderMap, key string) string {
	if headers == nil {
		return ""
	}
	kl := strings.ToLower(key)
	for _, h := range headers.Headers {
		if strings.ToLower(h.Key) == kl {
			if len(h.RawValue) > 0 {
				return string(h.RawValue)
			}
			return h.Value
		}
	}
	return ""
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

func requestBodyContinueResponse() *extprocv3.ProcessingResponse {
	return &extprocv3.ProcessingResponse{
		Response: &extprocv3.ProcessingResponse_RequestBody{
			RequestBody: &extprocv3.BodyResponse{Response: continueCommon()},
		},
	}
}
