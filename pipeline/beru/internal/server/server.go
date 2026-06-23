package server

import (
	"context"
	"log/slog"

	beruv1 "github.com/shadow-diff/beru/pkg/api/beru/v1"
	"github.com/shadow-diff/beru/internal/ingest"
)

// TrafficReporter implements the Beru TrafficReporter gRPC service.
type TrafficReporter struct {
	beruv1.UnimplementedTrafficReporterServer
	Log   *slog.Logger
	Store *ingest.Store
}

func (s *TrafficReporter) ReportTraffic(ctx context.Context, req *beruv1.ReportTrafficRequest) (*beruv1.ReportTrafficResponse, error) {
	if req == nil || req.Report == nil {
		return &beruv1.ReportTrafficResponse{}, nil
	}
	report := req.Report
	go s.Store.Handle(report)
	return &beruv1.ReportTrafficResponse{}, nil
}
