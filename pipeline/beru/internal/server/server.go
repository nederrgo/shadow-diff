package server

import (
	"context"
	"log/slog"

	beruv1 "github.com/shadow-diff/beru/pkg/api/beru/v1"
	v2engine "github.com/shadow-diff/beru/internal/v2/engine"
	v2report "github.com/shadow-diff/beru/internal/v2/report"
)

// TrafficReporter implements the Beru TrafficReporter gRPC service.
type TrafficReporter struct {
	beruv1.UnimplementedTrafficReporterServer
	Log              *slog.Logger
	Router           *v2engine.TraceRouter
	DefaultShadowTest string
}

func (s *TrafficReporter) ReportTraffic(ctx context.Context, req *beruv1.ReportTrafficRequest) (*beruv1.ReportTrafficResponse, error) {
	if req == nil || req.Report == nil {
		return &beruv1.ReportTrafficResponse{}, nil
	}
	if s.Router != nil {
		if raw, err := v2report.FromTrafficReport(req.Report, s.DefaultShadowTest); err == nil {
			s.Router.Route(raw)
		}
	}
	return &beruv1.ReportTrafficResponse{}, nil
}
