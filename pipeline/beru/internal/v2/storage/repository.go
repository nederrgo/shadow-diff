package storage

import "context"

type TraceRepository interface {
	AppendReport(ctx context.Context, report *RawReport) ([]RawReport, error)
	SaveDiffVerdict(ctx context.Context, traceID string, verdict *VerdictState) error
	ListReports(ctx context.Context, traceID, protocol string) ([]RawReport, error)
	ListTraceGroups(ctx context.Context, shadowTestName string, limit int) ([]TraceGroup, error)
	GetVerdict(ctx context.Context, traceID string) (*VerdictState, error)
}
