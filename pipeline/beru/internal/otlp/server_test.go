package otlp

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
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

	"github.com/shadow-diff/beru/internal/egressdiff"
)

type recordingEgress struct {
	mu      sync.Mutex
	reports []egressdiff.Report
}

func (r *recordingEgress) Handle(report egressdiff.Report) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reports = append(r.reports, report)
}

func (r *recordingEgress) snapshot() []egressdiff.Report {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]egressdiff.Report, len(r.reports))
	copy(out, r.reports)
	return out
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
	rec := &recordingEgress{}
	client := startTestServer(t, &Server{Log: slog.Default(), EgressStore: rec})
	_, err := client.Export(context.Background(), &coltracepb.ExportTraceServiceRequest{})
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if len(rec.snapshot()) != 0 {
		t.Fatalf("expected no reports, got %d", len(rec.reports))
	}
}

func TestExport_mongoSpanIngested(t *testing.T) {
	rec := &recordingEgress{}
	client := startTestServer(t, &Server{Log: slog.Default(), EgressStore: rec})

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

	reports := rec.snapshot()
	if len(reports) != 1 {
		t.Fatalf("expected 1 report, got %d", len(reports))
	}
	r := reports[0]
	if r.TraceID != wantTrace {
		t.Fatalf("TraceID = %q, want %q", r.TraceID, wantTrace)
	}
	if r.Workload != "control-a" {
		t.Fatalf("Workload = %q", r.Workload)
	}
	if r.Protocol != "mongodb" {
		t.Fatalf("Protocol = %q", r.Protocol)
	}
	if string(r.Payload) != string(wantPayload) {
		t.Fatalf("Payload = %s, want %s", r.Payload, wantPayload)
	}
}

func TestExport_mongoSpanStableSemconv(t *testing.T) {
	rec := &recordingEgress{}
	client := startTestServer(t, &Server{Log: slog.Default(), EgressStore: rec})

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
	reports := rec.snapshot()
	if len(reports) != 1 || reports[0].Workload != "candidate" {
		t.Fatalf("got reports: %+v", reports)
	}
}

func TestExport_serviceNameRoleFallback(t *testing.T) {
	rec := &recordingEgress{}
	client := startTestServer(t, &Server{Log: slog.Default(), EgressStore: rec})

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
	reports := rec.snapshot()
	if len(reports) != 1 || reports[0].Workload != "control-a" {
		t.Fatalf("got reports: %+v", reports)
	}
}

func TestExport_skipsNonMongoSpan(t *testing.T) {
	rec := &recordingEgress{}
	client := startTestServer(t, &Server{Log: slog.Default(), EgressStore: rec})

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
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	store := egressdiff.NewStore(log, egressdiff.Config{
		TraceTTL:         time.Minute,
		MaxPendingTraces: 100,
		SweepInterval:    time.Hour,
		EgressWait:       time.Second,
	})
	client := startTestServer(t, &Server{Log: log, EgressStore: store})

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

	deadline := time.Now().Add(2 * time.Second)
	want := "No egress regression for Trace " + wantTrace + " (mongodb)"
	for time.Now().Before(deadline) {
		if bytes.Contains(buf.Bytes(), []byte(want)) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("expected %q in logs, got: %s", want, buf.String())
}

func TestExport_payloadIsValidJSON(t *testing.T) {
	rec := &recordingEgress{}
	client := startTestServer(t, &Server{Log: slog.Default(), EgressStore: rec})
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
	reports := rec.snapshot()
	if len(reports) != 1 || !json.Valid(reports[0].Payload) {
		t.Fatalf("invalid payload: %+v", reports)
	}
}

func TestHandleHTTP_mongoSpan(t *testing.T) {
	rec := &recordingEgress{}
	srv := &Server{Log: slog.Default(), EgressStore: rec}
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
	if len(rec.snapshot()) != 1 {
		t.Fatalf("expected 1 report, got %+v", rec.snapshot())
	}
}
