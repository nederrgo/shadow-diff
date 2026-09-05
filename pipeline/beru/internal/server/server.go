package server

import (
	"context"
	"log/slog"

	"github.com/shadow-diff/beru/internal/engine"
	"github.com/shadow-diff/beru/internal/report"
	beruv1 "github.com/shadow-diff/beru/pkg/api/beru/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TrafficReporter implements the Beru TrafficReporter gRPC service.
type TrafficReporter struct {
	beruv1.UnimplementedTrafficReporterServer
	Log               *slog.Logger
	Router            *engine.TraceRouter
	DefaultShadowTest string
}

func (s *TrafficReporter) ReportTraffic(ctx context.Context, req *beruv1.ReportTrafficRequest) (*beruv1.ReportTrafficResponse, error) {
	if req == nil || req.Report == nil {
		return &beruv1.ReportTrafficResponse{}, nil
	}
	if s.Router != nil {
		if raw, err := report.FromTrafficReport(req.Report, s.DefaultShadowTest); err == nil {
			if err := s.Router.Route(raw); err != nil {
				return nil, status.Errorf(codes.Unavailable, "wal append: %v", err)
			}
		}
	}
	return &beruv1.ReportTrafficResponse{}, nil
}
