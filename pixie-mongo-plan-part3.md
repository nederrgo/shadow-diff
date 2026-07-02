# Part 3 — Beru OTLP MongoDB Ingestion + E2E

## Goal
Re-enable the existing (but disabled) MongoDB OTLP processing path in Beru so it
consumes the spans that Pixie sends. Extract the traceparent from raw BSON bytes,
extract the pod role, and route to the diff engine. Re-add MongoDB egress assertions
to the E2E test scripts.

Prerequisite: Part 2 is merged and the bridge is shipping MongoDB OTLP spans to
beru-local. The spans are currently dropped with "deprecated" log.

---

## 1. `ExtractTraceparentFromRaw` — new helper

File: `pipeline/beru/internal/otlp/mongo_parser.go`

Add at the bottom of the file:

```go
var traceparentRE = regexp.MustCompile(
    `00-[0-9a-f]{32}-[0-9a-f]{16}-[0-9a-f]{2}`,
)

// ExtractTraceparentFromRaw scans raw TCP payload bytes for a W3C traceparent.
// The app writes traceparent into MongoDB $comment as a UTF-8 string; it appears
// as plaintext ASCII inside the BSON binary that Pixie ships as db.raw_payload.
func ExtractTraceparentFromRaw(raw string) string {
    return traceparentRE.FindString(raw)
}
```

---

## 2. Update `mongoHintsFromSpan` — extract role + test name + traceparent

File: `pipeline/beru/internal/otlp/server.go` (or wherever `mongoHintsFromSpan` lives)

The function currently returns `MongoHints`. Extend it to also return `podRole` and
`testName` extracted from the Pixie-added span attributes:

```go
func mongoHintsFromSpan(span *tracev1.Span) (hints report.MongoHints, podRole, testName string) {
    for _, attr := range span.GetAttributes() {
        switch attr.GetKey() {
        case "db.operation", "db.operation.name":
            hints.Operation = attr.GetValue().GetStringValue()
        case "db.mongodb.collection", "db.collection.name":
            hints.Collection = attr.GetValue().GetStringValue()
        case "db.raw_payload":
            raw := attr.GetValue().GetStringValue()
            if tp := ExtractTraceparentFromRaw(raw); tp != "" {
                hints.TraceID = tp   // override OTel span trace ID with app trace ID
            }
        case "shadow.pod_role":
            podRole = attr.GetValue().GetStringValue()
        case "shadow.test_name":
            testName = attr.GetValue().GetStringValue()
        }
    }
    return
}
```

---

## 3. Re-enable MongoDB routing in `Export()`

File: `pipeline/beru/internal/otlp/server.go`

Remove the MongoDB skip block. Replace with routing:

```go
// Before (delete this):
var mongoSkipped int
for _, rs := range req.GetResourceSpans() {
    for _, ss := range rs.GetScopeSpans() {
        for _, span := range ss.GetSpans() {
            if isMongoSpan(span) {
                mongoSkipped++
            }
        }
    }
}
if mongoSkipped > 0 {
    log.Debug("OTLP Mongo egress deprecated; use POST /api/v1/ingest/wire", ...)
}

// After — route MongoDB spans:
for _, rs := range req.GetResourceSpans() {
    for _, ss := range rs.GetScopeSpans() {
        for _, span := range ss.GetSpans() {
            if !isMongoSpan(span) {
                continue
            }
            hints, podRole, testName := mongoHintsFromSpan(span)
            if hints.TraceID == "" {
                continue  // no traceparent in payload — skip
            }
            s.router.Route(report.FromMongoEgress(hints, podRole, testName))
        }
    }
}
```

> `report.FromMongoEgress` already exists in
> `pipeline/beru/internal/v2/report/egress.go`. Check its signature and add
> `podRole`/`testName` parameters if they are not already there, or adjust
> accordingly to match the `NetworkEventEnvelope` fields `PodRole` and
> `ShadowTestName`.

---

## 4. Re-add MongoDB egress assertions in E2E scripts

### `testing/scripts/e2e-http-mongo-test.sh`

Add after the RabbitMQ egress check (before `trap - EXIT`):

```bash
mongo_msg="No egress regression for Trace ${TRACE_HEX} (mongodb)"
echo "==> Verify Beru MongoDB egress (beru-local)"
if ! wait_beru_log "$SHADOW_NS" "$beru_local" "$mongo_msg" 60; then
  log_fail "Beru missing '${mongo_msg}' in ${SHADOW_NS}"
  kubectl logs -n "$SHADOW_NS" "$beru_local" --tail=80 >&2 || true
  exit 1
fi
log_success "Beru mongo egress: ${mongo_msg}"
```

### `testing/scripts/e2e-rmq-mongo-test.sh`

Same block, also before `trap - EXIT`.

### `testing/scripts/e2e-mongo-egress-test.sh`

The existing Envoy config check (line ~172) verifies required tokens. No changes
needed there — `mongo_egress` listener is gone (Part 1), but the OTLP-based test
still passes via the Pixie → beru path.

If the test currently has `mongo_proxy` in the forbidden list, leave it — it should
not be present since Part 1 removed the listener.

---

## Verification

### Unit

```bash
go test ./pipeline/beru/internal/otlp/...
# TestExport_mongoSpan should pass (no longer dropped)
# TestExtractTraceparentFromRaw covers the regex helper
```

### Integration (Pixie cluster)

Trigger a shadow test that has MongoDB dependency and watch beru-local logs:

```bash
kubectl logs -n shadow-default-http-mongo-test-shadow deploy/beru-local -f | grep -i mongo
# Expected: "No egress regression for Trace <hex> (mongodb)"
```

### Full E2E

```bash
SKIP_BUILD=1 ./testing/scripts/e2e-http-mongo-test.sh
SKIP_BUILD=1 ./testing/scripts/e2e-rmq-mongo-test.sh
SKIP_BUILD=1 ./testing/scripts/e2e-mongo-egress-test.sh
```

All three should pass including the MongoDB egress assertion.

---

## End-to-end data flow (complete)

```
App → mongodb://mongodb-control-a.<shadow-ns>.svc.cluster.local:27017
                     ↓
            MongoDB shadow service
                     ↓  (kernel-level TCP visible to eBPF)
     Pixie tcp_events  namespace=shadow-ns  remote_port=27017
     filter: contains(req, 'traceparent')
     derive: pod_role from pod name (-control-a- / -control-b- / -candidate-)
                     ↓  px.export  OTLP/gRPC  (insecure)
     beru-local :4317   shadow-ns
                     ↓  Export() → isMongoSpan → mongoHintsFromSpan
                     ↓  ExtractTraceparentFromRaw → TraceID
                     ↓  podRole="control-a|control-b|candidate"
     TraceRouter → diff engine
                     ↓
     "No egress regression for Trace X (mongodb)"
```
