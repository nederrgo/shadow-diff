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
	span := &tracepb.Span{
		Attributes: []*commonpb.KeyValue{
			kvString("http.request.method", "GET"),
			kvString("url.path", "/get"),
			kvString("http.host", "egress-httpbin.default.svc.cluster.local"),
			kvString("http.request.body", `{"a":1}`),
			kvInt("http.response.status_code", 200),
			kvString("http.response.body", "ok"),
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
