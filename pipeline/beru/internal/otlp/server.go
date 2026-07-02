package otlp

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"

	"github.com/shadow-diff/beru/internal/roles"
	"github.com/shadow-diff/beru/internal/trace"
	v2engine "github.com/shadow-diff/beru/internal/v2/engine"
	v2report "github.com/shadow-diff/beru/internal/v2/report"
)

const (
	attrShadowRole        = "shadow_role"
	attrServiceName       = "service.name"
	attrDBSystem          = "db.system"
	attrDBSystemName      = "db.system.name"
	attrDBStatement       = "db.statement"
	attrDBQueryText       = "db.query.text"
	attrDBOperation       = "db.operation"
	attrDBOperationName   = "db.operation.name"
	attrDBMongoCollection = "db.mongodb.collection"
	attrDBCollectionName  = "db.collection.name"
	attrDBRawPayload      = "db.raw_payload"
	attrK8sPodName        = "k8s.pod.name"
)

// Server implements the OpenTelemetry TraceService gRPC receiver.
type Server struct {
	coltracepb.UnimplementedTraceServiceServer
	Log               *slog.Logger
	Router            *v2engine.TraceRouter
	DefaultShadowTest string
	// seenPixieSpans deduplicates re-exports of the same physical event.
	// Pixie's rolling window causes the bridge to re-export the same event multiple times.
	// Key: "traceID:role:startNs". Only used for Pixie spans (startNs > 0).
	seenPixieSpans sync.Map
}

// Export receives OTLP span batches and routes MongoDB egress spans from Pixie eBPF capture.
func (s *Server) Export(ctx context.Context, req *coltracepb.ExportTraceServiceRequest) (*coltracepb.ExportTraceServiceResponse, error) {
	log := s.Log
	if log == nil {
		log = slog.Default()
	}
	if req == nil || s.Router == nil {
		return &coltracepb.ExportTraceServiceResponse{}, nil
	}
	var totalSpans, mongoSpans int
	for _, rs := range req.GetResourceSpans() {
		for _, ss := range rs.GetScopeSpans() {
			for _, span := range ss.GetSpans() {
				totalSpans++
				if isMongoSpan(span) {
					mongoSpans++
				}
			}
		}
	}
	if totalSpans > 0 {
		log.Info("OTLP export received", "spans", totalSpans, "mongo_spans", mongoSpans)
	}

	for _, rs := range req.GetResourceSpans() {
		resource := rs.GetResource()
		role := shadowRoleFromResource(resource)
		for _, ss := range rs.GetScopeSpans() {
			for _, span := range ss.GetSpans() {
				if !isMongoSpan(span) {
					continue
				}
				s.routeMongoSpan(log, resource, role, span)
			}
		}
	}
	_ = ctx
	return &coltracepb.ExportTraceServiceResponse{}, nil
}

func (s *Server) routeMongoSpan(log *slog.Logger, resource *resourcepb.Resource, role string, span *tracepb.Span) {
	rawPayload := rawPayloadAttr(span.GetAttributes())

	// Trace ID: prefer traceparent embedded in raw MongoDB wire bytes (Pixie eBPF path).
	// The app inserts traceparent into a BSON $comment field; it appears as plaintext ASCII.
	// Fallback: use the OTLP span TraceId (OTel SDK path).
	var traceID string
	if tp := ExtractTraceparentFromRaw(rawPayload); tp != "" {
		if tid, ok := trace.ParseTraceparent(tp); ok {
			traceID = tid
		}
	}
	if traceID == "" {
		if tid, ok := traceIDHex(span.GetTraceId()); ok && tid != "00000000000000000000000000000000" {
			traceID = tid
		}
	}
	if traceID == "" {
		log.Warn("OTLP Mongo: no trace ID in span or raw payload — skipped",
			"span", span.GetName(),
			"raw_payload_len", len(rawPayload),
			"otlp_trace_id", hex.EncodeToString(span.GetTraceId()))
		return
	}

	if role == "" {
		log.Warn("OTLP Mongo: could not derive shadow role — skipped",
			"traceID", traceID,
			"pod", stringAttr(resource.GetAttributes(), attrK8sPodName),
			"service", stringAttr(resource.GetAttributes(), attrServiceName))
		return
	}

	// Deduplicate Pixie re-exports: the bridge runs every 3s over a rolling window, so the
	// same physical MongoDB event is exported multiple times. Use span start_time (set from
	// Pixie's df.time_ = actual syscall timestamp) as a unique-per-event key.
	if startNs := span.GetStartTimeUnixNano(); startNs > 0 {
		dedupKey := fmt.Sprintf("%s:%s:%d", traceID, role, startNs)
		if _, seen := s.seenPixieSpans.LoadOrStore(dedupKey, struct{}{}); seen {
			return
		}
	}

	// Shadow test name: parse from pod name pattern "<test>-<role>-<hash>".
	// Pixie captures MongoDB events on server pods ("mongodb-control-a-..."), not worker pods,
	// so the pod name extraction yields the dependency prefix ("mongodb"). Fall back to
	// DefaultShadowTest when the extracted name is a known infrastructure component name.
	pod := stringAttr(resource.GetAttributes(), attrK8sPodName)
	shadowTestName := shadowTestNameFromPodName(pod, role)
	if shadowTestName == "" || isKnownDependencyName(shadowTestName) {
		shadowTestName = s.DefaultShadowTest
	}

	hints := mongoHintsFromSpan(span)

	// Pixie mongodb_events.req_body concatenates the command BSON and document BSON
	// as two separate JSON objects: "{command} {document}".
	// The command doc drives signature derivation (insert:orders); the document body
	// is the actual record being written and is what the diff engine should compare.
	cmdDoc := extractFirstJSONObject(rawPayload)
	docBody := extractSecondJSONObject(rawPayload)

	// Pre-populate hints from the command doc so MongoSignature can produce a valid
	// signature even when the comparison payload is the document body (which has no
	// command keys like "insert"/"find").
	if len(cmdDoc) > 0 && hints.Operation == "" {
		if sig := v2report.MongoSignature(cmdDoc, v2report.MongoHints{}); sig != "" {
			if parts := strings.SplitN(sig, ":", 3); len(parts) == 3 && parts[0] == "mongodb" && parts[1] != "unknown" {
				hints.Operation = parts[1]
				hints.Collection = parts[2]
			}
		}
	}

	// Use document body as comparison payload; fall back to command doc when there
	// is no second object (e.g. find/count commands that carry no document body).
	payload := docBody
	if len(payload) == 0 {
		payload = cmdDoc
	}
	if len(payload) == 0 && rawPayload != "" {
		payload = []byte(rawPayload)
	}
	if len(payload) == 0 {
		if stmt := mongoDBStatement(span); stmt != "" {
			if parsed, err := ParseMongoStatement(stmt); err == nil {
				payload = parsed
			}
		}
	}

	report, err := v2report.FromMongoEgress(traceID, role, shadowTestName, payload, hints)
	if err != nil {
		log.Warn("OTLP Mongo: build report failed", "traceID", traceID, "err", err)
		return
	}
	s.Router.Route(report)
	log.Info("OTLP Mongo: routed to diff engine", "traceID", traceID, "role", role, "shadowTest", shadowTestName, "signature", report.Signature)
}

func traceIDHex(raw []byte) (string, bool) {
	if len(raw) != 16 {
		return "", false
	}
	return strings.ToLower(hex.EncodeToString(raw)), true
}

func stringAttr(attrs []*commonpb.KeyValue, key string) string {
	for _, kv := range attrs {
		if kv == nil || kv.GetKey() != key {
			continue
		}
		if v := kv.GetValue(); v != nil {
			return strings.TrimSpace(v.GetStringValue())
		}
	}
	return ""
}

// rawPayloadAttr reads db.raw_payload, handling Pixie's BytesValue encoding for raw TCP frames.
func rawPayloadAttr(attrs []*commonpb.KeyValue) string {
	for _, kv := range attrs {
		if kv == nil || kv.GetKey() != attrDBRawPayload {
			continue
		}
		v := kv.GetValue()
		if v == nil {
			continue
		}
		if b := v.GetBytesValue(); len(b) > 0 {
			return string(b)
		}
		return strings.TrimSpace(v.GetStringValue())
	}
	return ""
}

func shadowRoleFromResource(res *resourcepb.Resource) string {
	if res == nil {
		return ""
	}
	role := stringAttr(res.GetAttributes(), attrShadowRole)
	if roles.IsValid(role) {
		return role
	}
	// Pixie: derive role from pod name pattern "<test>-<role>-<replicaset>-<hash>"
	if pod := stringAttr(res.GetAttributes(), attrK8sPodName); pod != "" {
		for _, r := range roles.All {
			if strings.Contains(pod, "-"+r+"-") {
				return r
			}
		}
	}
	serviceName := stringAttr(res.GetAttributes(), attrServiceName)
	for _, r := range roles.All {
		if strings.HasSuffix(serviceName, "-"+r) {
			return r
		}
	}
	return ""
}

// shadowTestNameFromPodName extracts the shadow test name from a pod name.
// Pod name format: "<test-name>-<role>-<replicaset>-<pod-hash>".
func shadowTestNameFromPodName(pod, role string) string {
	if pod == "" || role == "" {
		return ""
	}
	suffix := "-" + role + "-"
	if idx := strings.Index(pod, suffix); idx > 0 {
		return pod[:idx]
	}
	return ""
}

// isKnownDependencyName returns true when pod name extraction yields a well-known
// infrastructure component prefix rather than a shadow test name.
// Monarch names dependency pods as "<component>-<role>-<rs>-<hash>" (e.g., "mongodb-control-a-...").
var knownDependencyNames = map[string]struct{}{
	"mongodb": {}, "rabbitmq": {}, "postgres": {}, "postgresql": {},
	"redis": {}, "mysql": {}, "kafka": {}, "elasticsearch": {},
}

func isKnownDependencyName(name string) bool {
	_, ok := knownDependencyNames[strings.ToLower(name)]
	return ok
}

// extractFirstJSONObject reads the first valid JSON object from s (which may be
// multiple BSON-as-JSON objects concatenated, as Pixie's req_body produces).
func extractFirstJSONObject(s string) []byte {
	if s == "" {
		return nil
	}
	var raw json.RawMessage
	dec := json.NewDecoder(strings.NewReader(s))
	if err := dec.Decode(&raw); err != nil {
		return nil
	}
	return []byte(raw)
}

// extractSecondJSONObject reads the second JSON object from a concatenated payload
// such as the one Pixie produces for mongodb_events.req_body: "{command} {document}".
func extractSecondJSONObject(s string) []byte {
	if s == "" {
		return nil
	}
	dec := json.NewDecoder(strings.NewReader(s))
	var first json.RawMessage
	if err := dec.Decode(&first); err != nil {
		return nil
	}
	var second json.RawMessage
	if err := dec.Decode(&second); err != nil {
		return nil
	}
	return []byte(second)
}

func stringAttrFirst(attrs []*commonpb.KeyValue, keys ...string) string {
	for _, key := range keys {
		if v := stringAttr(attrs, key); v != "" {
			return v
		}
	}
	return ""
}

func mongoDBSystem(span *tracepb.Span) string {
	return stringAttrFirst(span.GetAttributes(), attrDBSystem, attrDBSystemName)
}

func mongoDBStatement(span *tracepb.Span) string {
	return stringAttrFirst(span.GetAttributes(), attrDBStatement, attrDBQueryText)
}

func mongoHintsFromSpan(span *tracepb.Span) v2report.MongoHints {
	hints := v2report.MongoHints{
		Operation:  stringAttrFirst(span.GetAttributes(), attrDBOperation, attrDBOperationName),
		Collection: stringAttrFirst(span.GetAttributes(), attrDBMongoCollection, attrDBCollectionName),
	}
	if hints.Operation == "" {
		hints.Operation = v2report.MongoOperationFromStatement(mongoDBStatement(span))
	}
	return hints
}

func isMongoSpan(span *tracepb.Span) bool {
	if span == nil {
		return false
	}
	if mongoDBSystem(span) != "mongodb" {
		return false
	}
	// Pixie-sourced span: raw TCP wire payload instead of db.statement
	if rawPayloadAttr(span.GetAttributes()) != "" {
		return true
	}
	if mongoDBStatement(span) != "" {
		return true
	}
	hints := mongoHintsFromSpan(span)
	return hints.Operation != "" && hints.Collection != ""
}
