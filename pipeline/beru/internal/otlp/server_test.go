package otlp

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"

	v2engine "github.com/shadow-diff/beru/internal/v2/engine"
	v2storage "github.com/shadow-diff/beru/internal/v2/storage"
)

type routeRecorder struct {
	mu       sync.Mutex
	reports  []v2storage.RawReport
	verdicts map[string]v2storage.VerdictState
}

func (r *routeRecorder) AppendReport(ctx context.Context, report *v2storage.RawReport) ([]v2storage.RawReport, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reports = append(r.reports, *report)
	out := append([]v2storage.RawReport(nil), r.reports...)
	return out, nil
}

func (r *routeRecorder) SaveDiffVerdict(ctx context.Context, traceID string, verdict *v2storage.VerdictState) error {
	if verdict == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.verdicts == nil {
		r.verdicts = make(map[string]v2storage.VerdictState)
	}
	r.verdicts[traceID] = *verdict
	return nil
}

func (r *routeRecorder) ListReports(ctx context.Context, traceID, protocol string) ([]v2storage.RawReport, error) {
	return r.snapshot(), nil
}

func (r *routeRecorder) ListTraceGroups(ctx context.Context, shadowTestName string, limit int) ([]v2storage.TraceGroup, error) {
	return nil, nil
}

func (r *routeRecorder) GetVerdict(ctx context.Context, traceID string) (*v2storage.VerdictState, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if v, ok := r.verdicts[traceID]; ok {
		out := v
		return &out, nil
	}
	return nil, nil
}

func (r *routeRecorder) snapshot() []v2storage.RawReport {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]v2storage.RawReport, len(r.reports))
	copy(out, r.reports)
	return out
}

func (r *routeRecorder) verdict(traceID string) (v2storage.VerdictState, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	v, ok := r.verdicts[traceID]
	return v, ok
}

func waitForReports(t *testing.T, rec *routeRecorder, n int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(rec.snapshot()) >= n {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("expected >= %d reports, got %d", n, len(rec.snapshot()))
}

func waitForVerdict(t *testing.T, rec *routeRecorder, traceID, wantStatus string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if v, ok := rec.verdict(traceID); ok && v.Status == wantStatus {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	v, _ := rec.verdict(traceID)
	t.Fatalf("expected verdict %q for trace %s, got %+v reports=%d", wantStatus, traceID, v, len(rec.snapshot()))
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
	if len(rec.snapshot()) != 0 {
		t.Fatalf("expected no reports, got %d", len(rec.reports))
	}
}

func TestExport_mongoSpanIngested(t *testing.T) {
	rec := &routeRecorder{}
	client := startTestServer(t, &Server{Log: slog.Default(), Router: testRouter(rec)})

	traceBytes := bytes.Repeat([]byte{0x01}, 16)
	wantTrace := "01010101010101010101010101010101"
	stmt := `{"insert": "collection", "documents": [{"id": 123}]}`
	wantPayload, err := ParseMongoStatement(stmt)
	if err != nil {
		t.Fatal(err)
	}

	req := &coltracepb.ExportTraceServiceRequest{
		ResourceSpans: []*tracepb.ResourceSpans{{
			Resource: &resourcepb.Resource{
				Attributes: []*commonpb.KeyValue{
					kvString(attrShadowRole, "control-a"),
				},
			},
			ScopeSpans: []*tracepb.ScopeSpans{{
				Spans: []*tracepb.Span{{
					TraceId: traceBytes,
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

	waitForReports(t, rec, 1)
	reports := rec.snapshot()
	if len(reports) != 1 {
		t.Fatalf("expected 1 report, got %d", len(reports))
	}
	r := reports[0]
	if r.TraceID != wantTrace {
		t.Fatalf("TraceID = %q, want %q", r.TraceID, wantTrace)
	}
	if r.ShadowRole != "control-a" {
		t.Fatalf("ShadowRole = %q", r.ShadowRole)
	}
	if r.Protocol != "mongodb" {
		t.Fatalf("Protocol = %q", r.Protocol)
	}
	if string(r.PayloadBytes) != string(wantPayload) {
		t.Fatalf("PayloadBytes = %s, want %s", r.PayloadBytes, wantPayload)
	}
	if r.Signature != "mongodb:insert:collection" {
		t.Fatalf("Signature = %q, want mongodb:insert:collection", r.Signature)
	}
}

func TestExport_mongoSpanStableSemconv(t *testing.T) {
	rec := &routeRecorder{}
	client := startTestServer(t, &Server{Log: slog.Default(), Router: testRouter(rec)})

	stmt := `{"insert":"items","documents":[{"id":"e2e"}]}`
	req := &coltracepb.ExportTraceServiceRequest{
		ResourceSpans: []*tracepb.ResourceSpans{{
			Resource: &resourcepb.Resource{
				Attributes: []*commonpb.KeyValue{
					kvString(attrServiceName, "mongo-test-shadow-candidate"),
				},
			},
			ScopeSpans: []*tracepb.ScopeSpans{{
				Spans: []*tracepb.Span{{
					TraceId: bytes.Repeat([]byte{0x03}, 16),
					Attributes: []*commonpb.KeyValue{
						kvString(attrDBSystemName, "mongodb"),
						kvString(attrDBQueryText, stmt),
					},
				}},
			}},
		}},
	}
	if _, err := client.Export(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	waitForReports(t, rec, 1)
	reports := rec.snapshot()
	if len(reports) != 1 || reports[0].ShadowRole != "candidate" {
		t.Fatalf("got reports: %+v", reports)
	}
	if reports[0].Signature != "mongodb:insert:items" {
		t.Fatalf("Signature = %q", reports[0].Signature)
	}
}

func TestExport_serviceNameRoleFallback(t *testing.T) {
	rec := &routeRecorder{}
	client := startTestServer(t, &Server{Log: slog.Default(), Router: testRouter(rec)})

	req := &coltracepb.ExportTraceServiceRequest{
		ResourceSpans: []*tracepb.ResourceSpans{{
			Resource: &resourcepb.Resource{
				Attributes: []*commonpb.KeyValue{
					kvString(attrServiceName, "mongo-test-shadow-control-a"),
				},
			},
			ScopeSpans: []*tracepb.ScopeSpans{{
				Spans: []*tracepb.Span{{
					TraceId: bytes.Repeat([]byte{0x02}, 16),
					Attributes: []*commonpb.KeyValue{
						kvString(attrDBSystem, "mongodb"),
						kvString(attrDBStatement, `{"insert":"x"}`),
					},
				}},
			}},
		}},
	}
	if _, err := client.Export(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	waitForReports(t, rec, 1)
	reports := rec.snapshot()
	if len(reports) != 1 || reports[0].ShadowRole != "control-a" {
		t.Fatalf("got reports: %+v", reports)
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
	if len(rec.snapshot()) != 0 {
		t.Fatal("expected no reports for non-mongo span")
	}
}

func TestExport_threeRolesTriggersDiff(t *testing.T) {
	rec := &routeRecorder{}
	client := startTestServer(t, &Server{Log: slog.Default(), Router: testRouter(rec)})

	traceBytes := bytes.Repeat([]byte{0x04}, 16)
	wantTrace := "04040404040404040404040404040404"
	stmt := `{"insert":"c","documents":[{"id":1}]}`

	for _, role := range []string{"control-a", "control-b", "candidate"} {
		req := &coltracepb.ExportTraceServiceRequest{
			ResourceSpans: []*tracepb.ResourceSpans{{
				Resource: &resourcepb.Resource{
					Attributes: []*commonpb.KeyValue{kvString(attrShadowRole, role)},
				},
				ScopeSpans: []*tracepb.ScopeSpans{{
					Spans: []*tracepb.Span{{
						TraceId: traceBytes,
						Attributes: []*commonpb.KeyValue{
							kvString(attrDBSystem, "mongodb"),
							kvString(attrDBStatement, stmt),
						},
					}},
				}},
			}},
		}
		if _, err := client.Export(context.Background(), req); err != nil {
			t.Fatal(err)
		}
	}

	waitForVerdict(t, rec, wantTrace, "MATCH")
}

func TestExport_payloadIsValidJSON(t *testing.T) {
	rec := &routeRecorder{}
	client := startTestServer(t, &Server{Log: slog.Default(), Router: testRouter(rec)})
	stmt := `{"insert":"collection","documents":[{"id":123}]}`
	req := &coltracepb.ExportTraceServiceRequest{
		ResourceSpans: []*tracepb.ResourceSpans{{
			Resource: &resourcepb.Resource{
				Attributes: []*commonpb.KeyValue{kvString(attrShadowRole, "control-a")},
			},
			ScopeSpans: []*tracepb.ScopeSpans{{
				Spans: []*tracepb.Span{{
					TraceId: bytes.Repeat([]byte{0x05}, 16),
					Attributes: []*commonpb.KeyValue{
						kvString(attrDBSystem, "mongodb"),
						kvString(attrDBStatement, stmt),
					},
				}},
			}},
		}},
	}
	if _, err := client.Export(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	waitForReports(t, rec, 1)
	reports := rec.snapshot()
	if len(reports) != 1 || !json.Valid(reports[0].PayloadBytes) {
		t.Fatalf("invalid payload: %+v", reports)
	}
}

func TestHandleHTTP_mongoSpan(t *testing.T) {
	rec := &routeRecorder{}
	srv := &Server{Log: slog.Default(), Router: testRouter(rec)}
	stmt := `{"insert":"orders","documents":[{"order_id":"x"}]}`
	req := &coltracepb.ExportTraceServiceRequest{
		ResourceSpans: []*tracepb.ResourceSpans{{
			Resource: &resourcepb.Resource{
				Attributes: []*commonpb.KeyValue{kvString(attrShadowRole, "candidate")},
			},
			ScopeSpans: []*tracepb.ScopeSpans{{
				Spans: []*tracepb.Span{{
					TraceId: bytes.Repeat([]byte{0x07}, 16),
					Attributes: []*commonpb.KeyValue{
						kvString(attrDBSystem, "mongodb"),
						kvString(attrDBStatement, stmt),
					},
				}},
			}},
		}},
	}
	body, err := proto.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	rr := httptest.NewRecorder()
	httpReq := httptest.NewRequest(http.MethodPost, "/v1/traces", bytes.NewReader(body))
	httpReq.Header.Set("Content-Type", "application/x-protobuf")
	srv.HandleHTTP(rr, httpReq)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	waitForReports(t, rec, 1)
	if len(rec.snapshot()) != 1 {
		t.Fatalf("expected 1 report, got %+v", rec.snapshot())
	}
}

func TestExport_mongoSpanStableAttrsOnly(t *testing.T) {
	rec := &routeRecorder{}
	client := startTestServer(t, &Server{Log: slog.Default(), Router: testRouter(rec)})

	req := &coltracepb.ExportTraceServiceRequest{
		ResourceSpans: []*tracepb.ResourceSpans{{
			Resource: &resourcepb.Resource{
				Attributes: []*commonpb.KeyValue{kvString(attrShadowRole, "control-a")},
			},
			ScopeSpans: []*tracepb.ScopeSpans{{
				Spans: []*tracepb.Span{{
					TraceId: bytes.Repeat([]byte{0x08}, 16),
					Attributes: []*commonpb.KeyValue{
						kvString(attrDBSystemName, "mongodb"),
						kvString(attrDBOperationName, "insert"),
						kvString(attrDBCollectionName, "orders"),
					},
				}},
			}},
		}},
	}
	if _, err := client.Export(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	waitForReports(t, rec, 1)
	if rec.snapshot()[0].Signature != "mongodb:insert:orders" {
		t.Fatalf("Signature = %q", rec.snapshot()[0].Signature)
	}
}

func TestExport_mongoSpanFixtures(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "mongo_spans.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixtures []struct {
		Name          string            `json:"name"`
		Attrs         map[string]string `json:"attrs"`
		WantSignature string            `json:"want_signature"`
	}
	if err := json.Unmarshal(data, &fixtures); err != nil {
		t.Fatal(err)
	}

	for i, fx := range fixtures {
		rec := &routeRecorder{}
		client := startTestServer(t, &Server{Log: slog.Default(), Router: testRouter(rec)})

		attrs := make([]*commonpb.KeyValue, 0, len(fx.Attrs))
		for k, v := range fx.Attrs {
			attrs = append(attrs, kvString(k, v))
		}
		traceByte := byte(i + 1)
		req := &coltracepb.ExportTraceServiceRequest{
			ResourceSpans: []*tracepb.ResourceSpans{{
				Resource: &resourcepb.Resource{
					Attributes: []*commonpb.KeyValue{kvString(attrShadowRole, "control-a")},
				},
				ScopeSpans: []*tracepb.ScopeSpans{{
					Spans: []*tracepb.Span{{
						TraceId:    bytes.Repeat([]byte{traceByte}, 16),
						Attributes: attrs,
					}},
				}},
			}},
		}
		if _, err := client.Export(context.Background(), req); err != nil {
			t.Fatalf("%s: %v", fx.Name, err)
		}
		waitForReports(t, rec, 1)
		if got := rec.snapshot()[0].Signature; got != fx.WantSignature {
			t.Fatalf("%s: signature = %q, want %q", fx.Name, got, fx.WantSignature)
		}
	}
}
