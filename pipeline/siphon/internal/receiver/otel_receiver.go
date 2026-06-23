package receiver

import (
	"context"
	"log/slog"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"

	collogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"

	"github.com/shadow-diff/siphon/internal/forwarder"
)

type httpForwarder interface {
	Forward(ctx context.Context, record forwarder.HTTPRecord) error
}

// OTLPReceiver implements shared OTLP log/trace ingestion and forwarding.
type OTLPReceiver struct {
	fwd      httpForwarder
	jobs     chan forwarder.HTTPRecord
	wg       sync.WaitGroup
	dropped  atomic.Uint64
	log      *slog.Logger
	stopOnce sync.Once
}

func NewOTLPReceiver(fwd httpForwarder, workers, queueSize int, log *slog.Logger) *OTLPReceiver {
	if workers <= 0 {
		workers = 8
	}
	if queueSize <= 0 {
		queueSize = 1024
	}
	if log == nil {
		log = slog.Default()
	}
	r := &OTLPReceiver{
		fwd:  fwd,
		jobs: make(chan forwarder.HTTPRecord, queueSize),
		log:  log,
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
		if err := r.fwd.Forward(context.Background(), record); err != nil {
			r.log.Warn("forward to igris failed", "err", err, "uri", record.RequestURI)
		}
	}
}

func (r *OTLPReceiver) ExportLogs(ctx context.Context, req *collogspb.ExportLogsServiceRequest) (*collogspb.ExportLogsServiceResponse, error) {
	_ = ctx
	if req == nil {
		return &collogspb.ExportLogsServiceResponse{}, nil
	}
	for _, rl := range req.GetResourceLogs() {
		for _, sl := range rl.GetScopeLogs() {
			for _, lr := range sl.GetLogRecords() {
				record, ok := parseHTTPRecord(lr, rl.GetResource())
				if !ok {
					continue
				}
				select {
				case r.jobs <- record:
				default:
					// ponytail: drop on full queue; upgrade path = metric + larger queue
					r.dropped.Add(1)
				}
			}
		}
	}
	return &collogspb.ExportLogsServiceResponse{}, nil
}

func (r *OTLPReceiver) ExportTraces(ctx context.Context, req *coltracepb.ExportTraceServiceRequest) (*coltracepb.ExportTraceServiceResponse, error) {
	_ = ctx
	if req == nil {
		return &coltracepb.ExportTraceServiceResponse{}, nil
	}
	for _, rs := range req.GetResourceSpans() {
		for _, ss := range rs.GetScopeSpans() {
			for _, span := range ss.GetSpans() {
				record, ok := parseHTTPRecordFromSpan(span, rs.GetResource())
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

func parseHTTPRecord(lr *logspb.LogRecord, res *resourcepb.Resource) (forwarder.HTTPRecord, bool) {
	if lr == nil {
		return forwarder.HTTPRecord{}, false
	}
	attrs := mergeAttrs(res, lr.GetAttributes())
	body := lr.GetBody().GetBytesValue()
	return parseHTTPRecordFromAttrs(attrs, body)
}

func parseHTTPRecordFromSpan(span *tracepb.Span, res *resourcepb.Resource) (forwarder.HTTPRecord, bool) {
	if span == nil {
		return forwarder.HTTPRecord{}, false
	}
	return parseHTTPRecordFromAttrs(mergeAttrs(res, span.GetAttributes()), nil)
}

func parseHTTPRecordFromAttrs(attrs map[string]string, logBody []byte) (forwarder.HTTPRecord, bool) {
	method := firstAttr(attrs, "http.request.method", "http.method")
	requestURI := requestURIFromAttrs(attrs)
	if requestURI == "" {
		return forwarder.HTTPRecord{}, false
	}

	body := logBody
	if len(body) == 0 {
		if s := firstAttr(attrs, "http.request.body"); s != "" {
			body = []byte(s)
		}
	}

	return forwarder.HTTPRecord{
		Method:        method,
		RequestURI:    requestURI,
		Host:          firstAttr(attrs, "http.host", "server.address", "url.domain"),
		Body:          body,
		ShadowTraceID: firstAttr(attrs, headerShadowTraceID, "http.request.header.x-shadow-trace-id"),
		Traceparent:   firstAttr(attrs, headerTraceparent, "http.request.header.traceparent"),
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

func requestURIFromAttrs(attrs map[string]string) string {
	if full := strings.TrimSpace(attrs["url.full"]); full != "" {
		if u, err := url.Parse(full); err == nil {
			path := u.Path
			if path == "" {
				path = "/"
			}
			if u.RawQuery != "" {
				return path + "?" + u.RawQuery
			}
			return path
		}
	}
	path := firstAttr(attrs, "url.path", "http.target")
	if path == "" {
		return ""
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + strings.TrimPrefix(path, "/")
	}
	if q := strings.TrimSpace(attrs["url.query"]); q != "" {
		return path + "?" + strings.TrimPrefix(q, "?")
	}
	return path
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

const (
	headerShadowTraceID = "x-shadow-trace-id"
	headerTraceparent   = "traceparent"
)
