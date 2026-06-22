package receiver

import (
	"context"

	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
)

type traceService struct {
	coltracepb.UnimplementedTraceServiceServer
	recv *OTLPReceiver
}

func (s *traceService) Export(ctx context.Context, req *coltracepb.ExportTraceServiceRequest) (*coltracepb.ExportTraceServiceResponse, error) {
	return s.recv.ExportTraces(ctx, req)
}

// NewTraceService returns a TraceServiceServer sharing recv's worker pool.
func NewTraceService(recv *OTLPReceiver) coltracepb.TraceServiceServer {
	return &traceService{recv: recv}
}
