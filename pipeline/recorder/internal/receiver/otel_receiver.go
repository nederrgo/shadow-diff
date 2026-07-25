package receiver

import (
	"context"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"

	"github.com/shadow-diff/recorder/internal/parse"
	"github.com/shadow-diff/recorder/internal/sample"
	"github.com/shadow-diff/recorder/internal/shop"
)

// OTLPReceiver ingests Pixie egress OTLP traces and posts to Shop.
type OTLPReceiver struct {
	shopClient       *shop.Client
	jobs             chan shop.RecordPayload
	wg               sync.WaitGroup
	dropped          atomic.Uint64
	samplePercentage int
	log              *slog.Logger
	stopOnce         sync.Once
}

func NewOTLPReceiver(client *shop.Client, workers, queueSize, samplePercentage int, log *slog.Logger) *OTLPReceiver {
	if workers <= 0 {
		workers = 4
	}
	if queueSize <= 0 {
		queueSize = 512
	}
	if samplePercentage <= 0 {
		samplePercentage = 100
	}
	if log == nil {
		log = slog.Default()
	}
	r := &OTLPReceiver{
		shopClient:       client,
		jobs:             make(chan shop.RecordPayload, queueSize),
		samplePercentage: samplePercentage,
		log:              log,
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
		r.shopClient.PostAsync(record)
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
				record, ok := parseEgressRecordFromSpan(span, rs.GetResource())
				if !ok || !r.admit(record.TraceID) {
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

func (r *OTLPReceiver) admit(traceID string) bool {
	return sample.SampledIn(traceID, r.samplePercentage)
}

func parseEgressRecordFromSpan(
	span *tracepb.Span,
	res *resourcepb.Resource,
) (shop.RecordPayload, bool) {
	if span == nil {
		return shop.RecordPayload{}, false
	}
	attrs := mergeAttrs(res, span.GetAttributes())

	host := firstAttr(attrs, "http.host", "server.address")
	if host == "" {
		return shop.RecordPayload{}, false
	}
	host = parse.NormalizeHTTPHost(host)

	path := firstAttr(attrs, "url.path", "http.target")
	if path == "" {
		return shop.RecordPayload{}, false
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

	// Prefer the W3C traceparent attribute stamped by the PxL script.
	// Tracing is required — do not fall back to Pixie span.TraceId for admission.
	traceID, ok := sample.TraceIDFromTraceparent(firstAttr(attrs, "traceparent"))
	if !ok {
		return shop.RecordPayload{}, false
	}

	return shop.RecordPayload{
		TraceID: traceID,
		Method:  method,
		Host:    host,
		Path:    path,
		Response: shop.RecordResponse{
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
