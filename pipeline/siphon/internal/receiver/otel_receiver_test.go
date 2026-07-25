package receiver

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	collogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"

	"github.com/shadow-diff/siphon/internal/forwarder"
)

func kvString(key, val string) *commonpb.KeyValue {
	return &commonpb.KeyValue{
		Key: key,
		Value: &commonpb.AnyValue{
			Value: &commonpb.AnyValue_StringValue{StringValue: val},
		},
	}
}

func TestParseHTTPRecord_attributeFallbacks(t *testing.T) {
	tp := "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	lr := &logspb.LogRecord{
		Attributes: []*commonpb.KeyValue{
			kvString("http.method", "GET"),
			kvString("url.path", "/v1/users"),
			kvString("url.query", "active=true"),
			kvString("traceparent", tp),
		},
		Body: &commonpb.AnyValue{
			Value: &commonpb.AnyValue_BytesValue{BytesValue: []byte("body")},
		},
	}
	rec, ok := parseHTTPRecord(lr, nil)
	if !ok {
		t.Fatal("expected record")
	}
	if rec.Method != "GET" {
		t.Fatalf("method %q", rec.Method)
	}
	if rec.RequestURI != "/v1/users?active=true" {
		t.Fatalf("uri %q", rec.RequestURI)
	}
	if rec.Traceparent != tp {
		t.Fatalf("traceparent %q", rec.Traceparent)
	}
	if string(rec.Body) != "body" {
		t.Fatalf("body %q", rec.Body)
	}
}

func TestParseHTTPRecordFromSpan_bodyAttribute(t *testing.T) {
	tp := "00-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-bbbbbbbbbbbbbbbb-01"
	span := &tracepb.Span{
		Attributes: []*commonpb.KeyValue{
			kvString("http.request.method", "POST"),
			kvString("url.path", "/echo"),
			kvString("http.request.body", `{"ok":true}`),
			kvString("traceparent", tp),
		},
	}
	rec, ok := parseHTTPRecordFromSpan(span, nil)
	if !ok {
		t.Fatal("expected record")
	}
	if rec.Method != "POST" {
		t.Fatalf("method %q", rec.Method)
	}
	if rec.RequestURI != "/echo" {
		t.Fatalf("uri %q", rec.RequestURI)
	}
	if string(rec.Body) != `{"ok":true}` {
		t.Fatalf("body %q", rec.Body)
	}
	if rec.Traceparent != tp {
		t.Fatalf("traceparent %q", rec.Traceparent)
	}
}

func TestExportTraces_enqueuesSpan(t *testing.T) {
	fwd := &stubForwarder{}
	r := NewOTLPReceiver(fwd, 1, 8, 100, nil)

	req := &coltracepb.ExportTraceServiceRequest{
		ResourceSpans: []*tracepb.ResourceSpans{{
			ScopeSpans: []*tracepb.ScopeSpans{{
				Spans: []*tracepb.Span{{
					Attributes: []*commonpb.KeyValue{
						kvString("url.path", "/from-pixie"),
						kvString("traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"),
					},
				}},
			}},
		}},
	}
	if _, err := r.ExportTraces(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	r.Stop()
	if fwd.called.Load() != 1 {
		t.Fatalf("forwarded %d want 1", fwd.called.Load())
	}
}

func TestParseHTTPRecord_urlFull(t *testing.T) {
	lr := &logspb.LogRecord{
		Attributes: []*commonpb.KeyValue{
			kvString("http.request.method", "POST"),
			kvString("url.full", "https://ignored.example/v2/items?x=1"),
		},
	}
	rec, ok := parseHTTPRecord(lr, nil)
	if !ok {
		t.Fatal("expected record")
	}
	if rec.RequestURI != "/v2/items?x=1" {
		t.Fatalf("uri %q", rec.RequestURI)
	}
}

type stubForwarder struct {
	called atomic.Uint64
}

func (s *stubForwarder) Forward(ctx context.Context, record forwarder.HTTPRecord) error {
	s.called.Add(1)
	return nil
}

func TestExport_enqueuesWithoutBlockingOnFullQueue(t *testing.T) {
	fwd := &stubForwarder{}
	r := NewOTLPReceiver(fwd, 1, 1, 100, nil)

	req := &collogspb.ExportLogsServiceRequest{
		ResourceLogs: []*logspb.ResourceLogs{{
			ScopeLogs: []*logspb.ScopeLogs{{
				LogRecords: []*logspb.LogRecord{
					{Attributes: []*commonpb.KeyValue{
						kvString("url.path", "/a"),
						kvString("traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"),
					}},
					{Attributes: []*commonpb.KeyValue{
						kvString("url.path", "/b"),
						kvString("traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"),
					}},
				},
			}},
		}},
	}

	start := time.Now()
	_, err := r.ExportLogs(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Fatalf("Export blocked for %v", elapsed)
	}
	if r.Dropped() != 1 {
		t.Fatalf("dropped %d want 1", r.Dropped())
	}
	r.Stop()
}
