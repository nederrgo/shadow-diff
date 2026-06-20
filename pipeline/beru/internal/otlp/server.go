package otlp

import (
	"context"
	"encoding/hex"
	"log/slog"
	"strings"

	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"

	"github.com/shadow-diff/beru/internal/egressdiff"
	"github.com/shadow-diff/beru/internal/roles"
)

const (
	attrShadowRole     = "shadow_role"
	attrServiceName    = "service.name"
	attrDBSystem       = "db.system"
	attrDBSystemName   = "db.system.name"
	attrDBStatement    = "db.statement"
	attrDBQueryText    = "db.query.text"
)

type egressHandler interface {
	Handle(report egressdiff.Report)
}

// Server implements the OpenTelemetry TraceService gRPC receiver.
type Server struct {
	coltracepb.UnimplementedTraceServiceServer
	Log         *slog.Logger
	EgressStore egressHandler
}

// Export receives OTLP span batches and routes MongoDB egress spans to the diff store.
func (s *Server) Export(ctx context.Context, req *coltracepb.ExportTraceServiceRequest) (*coltracepb.ExportTraceServiceResponse, error) {
	log := s.Log
	if log == nil {
		log = slog.Default()
	}
	if req == nil || s.EgressStore == nil {
		return &coltracepb.ExportTraceServiceResponse{}, nil
	}
	if n := len(req.GetResourceSpans()); n > 0 {
		var spanCount int
		for _, rs := range req.GetResourceSpans() {
			for _, ss := range rs.GetScopeSpans() {
				spanCount += len(ss.GetSpans())
			}
		}
		log.Debug("OTLP trace export received", "resourceSpans", n, "spans", spanCount)
	}

	var mongoSpans int
	var totalSpans int
	for _, rs := range req.GetResourceSpans() {
		shadowRole := shadowRoleFromResource(rs.GetResource())
		if shadowRole == "" {
			log.Debug("Skipping resource spans without shadow role")
			continue
		}
		for _, ss := range rs.GetScopeSpans() {
			for _, span := range ss.GetSpans() {
				totalSpans++
				if !isMongoSpan(span) {
					continue
				}
				traceID, ok := traceIDHex(span.GetTraceId())
				if !ok {
					log.Debug("Skipping mongo span with invalid trace id")
					continue
				}
				stmt := mongoDBStatement(span)
				payload, err := ParseMongoStatement(stmt)
				if err != nil {
					log.Debug("Skipping mongo span with unparseable db.statement", "err", err)
					continue
				}
				// ponytail: one payload per workload per trace; last mongo span wins
				s.EgressStore.Handle(egressdiff.Report{
					TraceID:  traceID,
					Workload: shadowRole,
					Protocol: "mongodb",
					Payload:  payload,
				})
				log.Info("Ingested OTLP MongoDB egress span", "trace", traceID, "role", shadowRole)
				mongoSpans++
			}
		}
	}
	if mongoSpans > 0 {
		log.Debug("OTLP export processed mongo spans", "count", mongoSpans)
	} else if totalSpans > 0 {
		log.Debug("OTLP export received non-mongo spans", "spans", totalSpans)
	}
	_ = ctx
	return &coltracepb.ExportTraceServiceResponse{}, nil
}

func traceIDHex(raw []byte) (string, bool) {
	if len(raw) != 16 {
		return "", false
	}
	return strings.ToLower(hex.EncodeToString(raw)), true
}

func stringAttr(attrs []*commonpb.KeyValue, key string) string {
	for _, kv := range attrs {
		if kv == nil || kv.GetKey() != key {
			continue
		}
		if v := kv.GetValue(); v != nil {
			return strings.TrimSpace(v.GetStringValue())
		}
	}
	return ""
}

func shadowRoleFromResource(res *resourcepb.Resource) string {
	if res == nil {
		return ""
	}
	role := stringAttr(res.GetAttributes(), attrShadowRole)
	if roles.IsValid(role) {
		return role
	}
	serviceName := stringAttr(res.GetAttributes(), attrServiceName)
	for _, r := range roles.All {
		if strings.HasSuffix(serviceName, "-"+r) {
			return r
		}
	}
	return ""
}

func stringAttrFirst(attrs []*commonpb.KeyValue, keys ...string) string {
	for _, key := range keys {
		if v := stringAttr(attrs, key); v != "" {
			return v
		}
	}
	return ""
}

func mongoDBSystem(span *tracepb.Span) string {
	return stringAttrFirst(span.GetAttributes(), attrDBSystem, attrDBSystemName)
}

func mongoDBStatement(span *tracepb.Span) string {
	return stringAttrFirst(span.GetAttributes(), attrDBStatement, attrDBQueryText)
}

func isMongoSpan(span *tracepb.Span) bool {
	if span == nil {
		return false
	}
	if mongoDBSystem(span) != "mongodb" {
		return false
	}
	return mongoDBStatement(span) != ""
}
