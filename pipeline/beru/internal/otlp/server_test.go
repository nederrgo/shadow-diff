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

func TestExport_mongoSpanRouted(t *testing.T) {
	rec := &routeRecorder{}
	client := startTestServer(t, &Server{Log: slog.Default(), Router: testRouter(rec), DefaultShadowTest: "mytest"})

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
	if len(rec.reports) != 1 {
		t.Fatalf("OTLP Mongo: expected 1 routed report, got %d", len(rec.reports))
	}
	if rec.reports[0].Protocol != "mongodb" {
		t.Fatalf("expected protocol=mongodb, got %q", rec.reports[0].Protocol)
	}
}

func TestExport_mongoSpanPixie(t *testing.T) {
	rec := &routeRecorder{}
	client := startTestServer(t, &Server{Log: slog.Default(), Router: testRouter(rec)})

	traceHex := "aabbccdd11223344aabbccdd11223344"
	spanHex := "aabbccdd11223344"
	traceparent := "00-" + traceHex + "-" + spanHex + "-01"
	rawPayload := `\x00\x00$comment` + traceparent + `\x00insert\x00orders\x00`

	req := &coltracepb.ExportTraceServiceRequest{
		ResourceSpans: []*tracepb.ResourceSpans{{
			Resource: &resourcepb.Resource{
				Attributes: []*commonpb.KeyValue{
					kvString(attrK8sPodName, "mytest-control-b-6c8f9d-x7k2p"),
				},
			},
			ScopeSpans: []*tracepb.ScopeSpans{{
				Spans: []*tracepb.Span{{
					TraceId: bytes.Repeat([]byte{0xff}, 16),
					Attributes: []*commonpb.KeyValue{
						kvString(attrDBSystem, "mongodb"),
						kvString(attrDBRawPayload, rawPayload),
					},
				}},
			}},
		}},
	}
	if _, err := client.Export(context.Background(), req); err != nil {
		t.Fatalf("Export: %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	if len(rec.reports) != 1 {
		t.Fatalf("Pixie Mongo: expected 1 routed report, got %d", len(rec.reports))
	}
	r := rec.reports[0]
	if r.TraceID != traceHex {
		t.Fatalf("expected traceID=%q, got %q", traceHex, r.TraceID)
	}
	if r.ShadowRole != "control-b" {
		t.Fatalf("expected role=control-b, got %q", r.ShadowRole)
	}
	if r.ShadowTestName != "mytest" {
		t.Fatalf("expected shadowTestName=mytest, got %q", r.ShadowTestName)
	}
}

func TestExport_deduplicatesPixieReExports(t *testing.T) {
	rec := &routeRecorder{}
	client := startTestServer(t, &Server{Log: slog.Default(), Router: testRouter(rec), DefaultShadowTest: "mytest"})

	stmt := `{"insert": "orders", "documents": [{"id": 1}]}`
	startNs := uint64(1720000000000000000)
	span := &tracepb.Span{
		TraceId:           bytes.Repeat([]byte{0x04}, 16),
		StartTimeUnixNano: startNs,
		Attributes: []*commonpb.KeyValue{
			kvString(attrDBSystem, "mongodb"),
			kvString(attrDBStatement, stmt),
		},
	}
	req := &coltracepb.ExportTraceServiceRequest{
		ResourceSpans: []*tracepb.ResourceSpans{{
			Resource: &resourcepb.Resource{
				Attributes: []*commonpb.KeyValue{kvString(attrShadowRole, "control-a")},
			},
			ScopeSpans: []*tracepb.ScopeSpans{{Spans: []*tracepb.Span{span}}},
		}},
	}

	// Export the same span twice (simulating Pixie's rolling window re-export).
	for i := 0; i < 2; i++ {
		if _, err := client.Export(context.Background(), req); err != nil {
			t.Fatalf("Export %d: %v", i, err)
		}
	}
	time.Sleep(100 * time.Millisecond)
	if len(rec.reports) != 1 {
		t.Fatalf("dedup: expected 1 report after 2 exports of the same span, got %d", len(rec.reports))
	}
}

func TestExport_mongoSpanPixieTwoObjects(t *testing.T) {
	rec := &routeRecorder{}
	client := startTestServer(t, &Server{Log: slog.Default(), Router: testRouter(rec), DefaultShadowTest: "mytest"})

	traceHex := "aabbccdd11223344aabbccdd11223344"
	spanHex := "aabbccdd11223344"
	traceparent := "00-" + traceHex + "-" + spanHex + "-01"
	// Two-object format: command doc + document body (as Pixie produces for mongodb_events.req_body)
	rawPayload := `{"insert":"orders","comment":"` + traceparent + `","$db":"test"} {"_id":"abc123","order_id":"e2e-1","status":"processed"}`

	req := &coltracepb.ExportTraceServiceRequest{
		ResourceSpans: []*tracepb.ResourceSpans{{
			Resource: &resourcepb.Resource{
				Attributes: []*commonpb.KeyValue{
					kvString(attrK8sPodName, "mytest-control-a-6c8f9d-x7k2p"),
				},
			},
			ScopeSpans: []*tracepb.ScopeSpans{{
				Spans: []*tracepb.Span{{
					TraceId: bytes.Repeat([]byte{0xaa}, 16),
					Attributes: []*commonpb.KeyValue{
						kvString(attrDBSystem, "mongodb"),
						kvString(attrDBRawPayload, rawPayload),
					},
				}},
			}},
		}},
	}
	if _, err := client.Export(context.Background(), req); err != nil {
		t.Fatalf("Export: %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	if len(rec.reports) != 1 {
		t.Fatalf("Pixie two-object: expected 1 routed report, got %d", len(rec.reports))
	}
	r := rec.reports[0]
	if r.TraceID != traceHex {
		t.Fatalf("expected traceID=%q, got %q", traceHex, r.TraceID)
	}
	if r.Signature != "mongodb:insert:orders" {
		t.Fatalf("expected signature=mongodb:insert:orders, got %q", r.Signature)
	}
	// PayloadBytes must be the document body (second object), not the command doc.
	wantPayload := `{"_id":"abc123","order_id":"e2e-1","status":"processed"}`
	if string(r.PayloadBytes) != wantPayload {
		t.Fatalf("expected PayloadBytes=%q, got %q", wantPayload, string(r.PayloadBytes))
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
