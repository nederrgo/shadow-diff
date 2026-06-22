package receiver

import (
	"context"

	collogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
)

// logsService adapts OTLPReceiver for LogsServiceServer (Export name collision with traces).
type logsService struct {
	collogspb.UnimplementedLogsServiceServer
	recv *OTLPReceiver
}

func (s *logsService) Export(ctx context.Context, req *collogspb.ExportLogsServiceRequest) (*collogspb.ExportLogsServiceResponse, error) {
	return s.recv.ExportLogs(ctx, req)
}

// traceService adapts OTLPReceiver for TraceServiceServer.
type traceService struct {
	coltracepb.UnimplementedTraceServiceServer
	recv *OTLPReceiver
}

func (s *traceService) Export(ctx context.Context, req *coltracepb.ExportTraceServiceRequest) (*coltracepb.ExportTraceServiceResponse, error) {
	return s.recv.ExportTraces(ctx, req)
}

// NewLogsService returns a LogsServiceServer sharing recv's worker pool.
func NewLogsService(recv *OTLPReceiver) collogspb.LogsServiceServer {
	return &logsService{recv: recv}
}

// NewTraceService returns a TraceServiceServer sharing recv's worker pool.
func NewTraceService(recv *OTLPReceiver) coltracepb.TraceServiceServer {
	return &traceService{recv: recv}
}
