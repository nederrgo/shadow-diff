package receiver

import (
	"context"
	"encoding/hex"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"

	"github.com/shadow-diff/recorder/internal/beru"
	"github.com/shadow-diff/recorder/internal/config"
	"github.com/shadow-diff/recorder/internal/parse"
)

// OTLPReceiver ingests Pixie egress OTLP traces and posts to Beru.
type OTLPReceiver struct {
	beru            *beru.Client
	recordAndReplay []config.RecordAndReplayHost
	jobs            chan beru.RecordPayload
	wg              sync.WaitGroup
	dropped         atomic.Uint64
	log             *slog.Logger
	stopOnce        sync.Once
}

func NewOTLPReceiver(client *beru.Client, hosts []config.RecordAndReplayHost, workers, queueSize int, log *slog.Logger) *OTLPReceiver {
	if workers <= 0 {
		workers = 4
	}
	if queueSize <= 0 {
		queueSize = 512
	}
	if log == nil {
		log = slog.Default()
	}
	r := &OTLPReceiver{
		beru:            client,
		recordAndReplay: hosts,
		jobs:            make(chan beru.RecordPayload, queueSize),
		log:             log,
	}
	for i := 0; i < workers; i++ {
		r.wg.Add(1)
		go r.worker()
	}
	return r
}

func (r *OTLPReceiver) Dropped() uint64 {
	return r.dropped.Load()
}

func (r *OTLPReceiver) Stop() {
	r.stopOnce.Do(func() {
		close(r.jobs)
		r.wg.Wait()
	})
}

func (r *OTLPReceiver) worker() {
	defer r.wg.Done()
	for record := range r.jobs {
		r.beru.PostAsync(record)
	}
}

func (r *OTLPReceiver) ExportTraces(ctx context.Context, req *coltracepb.ExportTraceServiceRequest) (*coltracepb.ExportTraceServiceResponse, error) {
	_ = ctx
	if req == nil {
		return &coltracepb.ExportTraceServiceResponse{}, nil
	}
	for _, rs := range req.GetResourceSpans() {
		for _, ss := range rs.GetScopeSpans() {
			for _, span := range ss.GetSpans() {
				record, ok := parseEgressRecordFromSpan(span, rs.GetResource(), r.recordAndReplay)
				if !ok {
					continue
				}
				select {
				case r.jobs <- record:
				default:
					r.dropped.Add(1)
				}
			}
		}
	}
	return &coltracepb.ExportTraceServiceResponse{}, nil
}

func parseEgressRecordFromSpan(
	span *tracepb.Span,
	res *resourcepb.Resource,
	hosts []config.RecordAndReplayHost,
) (beru.RecordPayload, bool) {
	if span == nil {
		return beru.RecordPayload{}, false
	}
	attrs := mergeAttrs(res, span.GetAttributes())

	host := firstAttr(attrs, "http.host", "server.address")
	if host == "" || !parse.HostMatches(host, hosts) {
		return beru.RecordPayload{}, false
	}
	host = parse.NormalizeHTTPHost(host)

	path := firstAttr(attrs, "url.path", "http.target")
	if path == "" {
		return beru.RecordPayload{}, false
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + strings.TrimPrefix(path, "/")
	}

	method := firstAttr(attrs, "http.request.method", "http.method")
	if method == "" {
		method = "GET"
	}

	status := 200
	if s := firstAttr(attrs, "http.response.status_code", "http.status_code"); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 {
			status = n
		}
	}

	respBody := firstAttr(attrs, "http.response.body")
	traceID := hex.EncodeToString(span.TraceId)

	return beru.RecordPayload{
		TraceID: traceID,
		Method:  method,
		Host:    host,
		Path:    path,
		Response: beru.RecordResponse{
			Status:  status,
			Headers: map[string]string{},
			Body:    respBody,
		},
	}, true
}

func mergeAttrs(res *resourcepb.Resource, recordAttrs []*commonpb.KeyValue) map[string]string {
	out := map[string]string{}
	for _, kv := range res.GetAttributes() {
		if k := strings.TrimSpace(kv.GetKey()); k != "" {
			out[k] = attrString(kv)
		}
	}
	for _, kv := range recordAttrs {
		if k := strings.TrimSpace(kv.GetKey()); k != "" {
			out[k] = attrString(kv)
		}
	}
	return out
}

func attrString(kv *commonpb.KeyValue) string {
	if kv == nil || kv.Value == nil {
		return ""
	}
	switch v := kv.Value.Value.(type) {
	case *commonpb.AnyValue_StringValue:
		return v.StringValue
	case *commonpb.AnyValue_BytesValue:
		return string(v.BytesValue)
	case *commonpb.AnyValue_IntValue:
		return strconv.FormatInt(v.IntValue, 10)
	default:
		return ""
	}
}

func firstAttr(attrs map[string]string, keys ...string) string {
	for _, k := range keys {
		if v := strings.TrimSpace(attrs[k]); v != "" {
			return v
		}
	}
	return ""
}
