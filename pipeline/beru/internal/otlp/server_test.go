package otlp

import (
	"bytes"
	"context"
	"log/slog"
	"net"
	"testing"
	"time"

	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	v2engine "github.com/shadow-diff/beru/internal/v2/engine"
	v2storage "github.com/shadow-diff/beru/internal/v2/storage"
)

type routeRecorder struct {
	reports []v2storage.RawReport
}

func (r *routeRecorder) AppendReport(ctx context.Context, report *v2storage.RawReport) ([]v2storage.RawReport, error) {
	r.reports = append(r.reports, *report)
	return r.reports, nil
}

func (r *routeRecorder) SaveDiffVerdict(ctx context.Context, traceID string, verdict *v2storage.VerdictState) error {
	return nil
}

func (r *routeRecorder) ListReports(ctx context.Context, traceID, protocol string) ([]v2storage.RawReport, error) {
	return r.reports, nil
}

func (r *routeRecorder) ListTraceGroups(ctx context.Context, shadowTestName string, limit int) ([]v2storage.TraceGroup, error) {
	return nil, nil
}

func (r *routeRecorder) GetVerdict(ctx context.Context, traceID string) (*v2storage.VerdictState, error) {
	return nil, nil
}

func testRouter(rec *routeRecorder) *v2engine.TraceRouter {
	return v2engine.NewTraceRouter(1, rec, nil)
}

func startTestServer(t *testing.T, srv *Server) coltracepb.TraceServiceClient {
	t.Helper()
	const bufSize = 1024 * 1024
	lis := bufconn.Listen(bufSize)
	grpcSrv := grpc.NewServer()
	coltracepb.RegisterTraceServiceServer(grpcSrv, srv)
	go func() { _ = grpcSrv.Serve(lis) }()
	t.Cleanup(grpcSrv.Stop)

	conn, err := grpc.NewClient("passthrough://bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
			return lis.Dial()
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return coltracepb.NewTraceServiceClient(conn)
}

func kvString(key, val string) *commonpb.KeyValue {
	return &commonpb.KeyValue{
		Key: key,
		Value: &commonpb.AnyValue{
			Value: &commonpb.AnyValue_StringValue{StringValue: val},
		},
	}
}

func TestExport_acceptsEmptyRequest(t *testing.T) {
	rec := &routeRecorder{}
	client := startTestServer(t, &Server{Log: slog.Default(), Router: testRouter(rec)})
	_, err := client.Export(context.Background(), &coltracepb.ExportTraceServiceRequest{})
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if len(rec.reports) != 0 {
		t.Fatalf("expected no reports, got %d", len(rec.reports))
	}
}

func TestExport_mongoSpanDeprecated(t *testing.T) {
	rec := &routeRecorder{}
	client := startTestServer(t, &Server{Log: slog.Default(), Router: testRouter(rec)})

	stmt := `{"insert": "collection", "documents": [{"id": 123}]}`
	req := &coltracepb.ExportTraceServiceRequest{
		ResourceSpans: []*tracepb.ResourceSpans{{
			Resource: &resourcepb.Resource{
				Attributes: []*commonpb.KeyValue{
					kvString(attrShadowRole, "control-a"),
				},
			},
			ScopeSpans: []*tracepb.ScopeSpans{{
				Spans: []*tracepb.Span{{
					TraceId: bytes.Repeat([]byte{0x01}, 16),
					Attributes: []*commonpb.KeyValue{
						kvString(attrDBSystem, "mongodb"),
						kvString(attrDBStatement, stmt),
					},
				}},
			}},
		}},
	}
	if _, err := client.Export(context.Background(), req); err != nil {
		t.Fatalf("Export: %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	if len(rec.reports) != 0 {
		t.Fatalf("OTLP Mongo deprecated: expected 0 reports, got %d", len(rec.reports))
	}
}

func TestExport_skipsNonMongoSpan(t *testing.T) {
	rec := &routeRecorder{}
	client := startTestServer(t, &Server{Log: slog.Default(), Router: testRouter(rec)})

	req := &coltracepb.ExportTraceServiceRequest{
		ResourceSpans: []*tracepb.ResourceSpans{{
			Resource: &resourcepb.Resource{
				Attributes: []*commonpb.KeyValue{kvString(attrShadowRole, "control-a")},
			},
			ScopeSpans: []*tracepb.ScopeSpans{{
				Spans: []*tracepb.Span{{
					TraceId: bytes.Repeat([]byte{0x03}, 16),
					Attributes: []*commonpb.KeyValue{
						kvString(attrDBSystem, "postgresql"),
						kvString(attrDBStatement, `{"select":1}`),
					},
				}},
			}},
		}},
	}
	if _, err := client.Export(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if len(rec.reports) != 0 {
		t.Fatal("expected no reports for non-mongo span")
	}
}
