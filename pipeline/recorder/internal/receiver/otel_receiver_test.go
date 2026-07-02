package receiver

import (
	"context"
	"testing"

	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"

	"github.com/shadow-diff/recorder/internal/beru"
	"github.com/shadow-diff/recorder/internal/config"
)

func kvString(key, val string) *commonpb.KeyValue {
	return &commonpb.KeyValue{
		Key: key,
		Value: &commonpb.AnyValue{
			Value: &commonpb.AnyValue_StringValue{StringValue: val},
		},
	}
}

func kvInt(key string, val int64) *commonpb.KeyValue {
	return &commonpb.KeyValue{
		Key: key,
		Value: &commonpb.AnyValue{
			Value: &commonpb.AnyValue_IntValue{IntValue: val},
		},
	}
}

func TestParseEgressRecordFromSpan_hostAllowlist(t *testing.T) {
	hosts := []config.RecordAndReplayHost{{Host: "egress-httpbin.default.svc.cluster.local"}}
	// Pixie-generated trace ID (different from the W3C trace ID below).
	traceIDBytes := []byte{0x4b, 0xf9, 0x2f, 0x35, 0x77, 0xb3, 0x4d, 0xa6, 0xa3, 0xce, 0x92, 0x9d, 0x0e, 0x0e, 0x47, 0x36}
	const w3cTraceID = "aabb00112233445566778899ccddeeff"
	span := &tracepb.Span{
		TraceId: traceIDBytes,
		Attributes: []*commonpb.KeyValue{
			kvString("http.request.method", "GET"),
			kvString("url.path", "/get"),
			kvString("http.host", "egress-httpbin.default.svc.cluster.local"),
			kvInt("http.response.status_code", 200),
			kvString("http.response.body", "ok"),
			kvString("traceparent", "00-"+w3cTraceID+"-00f067aa0ba902b7-01"),
		},
	}
	rec, ok := parseEgressRecordFromSpan(span, nil, hosts)
	if !ok {
		t.Fatal("expected record")
	}
	if rec.Host != "egress-httpbin.default.svc.cluster.local" {
		t.Fatalf("host %q", rec.Host)
	}
	if rec.Path != "/get" {
		t.Fatalf("path %q", rec.Path)
	}
	if rec.Response.Status != 200 || rec.Response.Body != "ok" {
		t.Fatalf("response %+v", rec.Response)
	}
	// W3C traceparent attribute takes priority over span.TraceId.
	if rec.TraceID != w3cTraceID {
		t.Fatalf("trace ID %q, want %q", rec.TraceID, w3cTraceID)
	}
}

func TestParseEgressRecordFromSpan_fallsBackToSpanTraceID(t *testing.T) {
	hosts := []config.RecordAndReplayHost{{Host: "egress-httpbin.default.svc.cluster.local"}}
	traceIDBytes := []byte{0x4b, 0xf9, 0x2f, 0x35, 0x77, 0xb3, 0x4d, 0xa6, 0xa3, 0xce, 0x92, 0x9d, 0x0e, 0x0e, 0x47, 0x36}
	span := &tracepb.Span{
		TraceId: traceIDBytes,
		Attributes: []*commonpb.KeyValue{
			kvString("http.request.method", "GET"),
			kvString("url.path", "/get"),
			kvString("http.host", "egress-httpbin.default.svc.cluster.local"),
			kvInt("http.response.status_code", 200),
		},
	}
	rec, ok := parseEgressRecordFromSpan(span, nil, hosts)
	if !ok {
		t.Fatal("expected record")
	}
	// No traceparent attribute — falls back to hex(span.TraceId).
	if rec.TraceID != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Fatalf("trace ID %q", rec.TraceID)
	}
}

func TestParseEgressRecordFromSpan_dropsIPHost(t *testing.T) {
	hosts := []config.RecordAndReplayHost{{Host: "egress-httpbin.default.svc.cluster.local"}}
	span := &tracepb.Span{
		Attributes: []*commonpb.KeyValue{
			kvString("http.host", "10.0.0.5"),
			kvString("url.path", "/"),
		},
	}
	if _, ok := parseEgressRecordFromSpan(span, nil, hosts); ok {
		t.Fatal("expected drop for IP host not in allowlist")
	}
}

func TestExportTraces_enqueuesAllowedHost(t *testing.T) {
	ch := make(chan beru.RecordPayload, 1)
	client := beru.NewClient("http://127.0.0.1:9")
	r := NewOTLPReceiver(client, []config.RecordAndReplayHost{
		{Host: "egress-httpbin.default.svc.cluster.local"},
	}, 1, 1, nil)
	r.jobs = ch

	req := &coltracepb.ExportTraceServiceRequest{
		ResourceSpans: []*tracepb.ResourceSpans{{
			ScopeSpans: []*tracepb.ScopeSpans{{
				Spans: []*tracepb.Span{{
					Attributes: []*commonpb.KeyValue{
						kvString("http.host", "egress-httpbin.default.svc.cluster.local"),
						kvString("url.path", "/get"),
					},
				}},
			}},
		}},
	}
	if _, err := r.ExportTraces(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	select {
	case rec := <-ch:
		if rec.Host != "egress-httpbin.default.svc.cluster.local" {
			t.Fatalf("host %q", rec.Host)
		}
	default:
		t.Fatal("expected enqueue")
	}
	r.Stop()
}
