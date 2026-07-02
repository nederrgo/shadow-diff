# Part 2 — Pixie MongoDB Extension

## Goal
Extend the existing Pixie eBPF infrastructure (already working for HTTP) to also
capture MongoDB wire traffic from shadow pods and ship it as OTLP spans to beru-local.

Prerequisite: Part 1 is merged and pods are healthy.

When this part is done, the pixie-stream-bridge daemon renders and runs a MongoDB PxL
script for every active ShadowTest that has a MongoDB dependency. beru-local receives
the OTLP spans but does not yet process them (that is Part 3).

---

## 1. New fields on `PixieStreamRuleSpec`

File: `api/v1alpha1/pixiestreamrule_types.go`

Add two fields after `ExcludePaths`:

```go
// ShadowNamespace is the shadow namespace (shadow-<cr-ns>-<cr-name>) where
// shadow pods run. Needed by the MongoDB PxL template to filter shadow-side
// traffic — distinct from TargetNamespace which is the production namespace.
// +optional
ShadowNamespace string `json:"shadowNamespace,omitempty"`

// MongoOTelEndpoint is the gRPC OTLP export destination for MongoDB egress spans.
// Non-empty activates the mongodb-export PxL script (tcp_events on port 27017).
// +optional
MongoOTelEndpoint string `json:"mongoOtelEndpoint,omitempty"`
```

Update `api/v1alpha1/zz_generated.deepcopy.go`:
- Add `out.ShadowNamespace = in.ShadowNamespace` and
  `out.MongoOTelEndpoint = in.MongoOTelEndpoint` inside `DeepCopyInto` for
  `PixieStreamRuleSpec` (or run `make generate`).

---

## 2. Populate the new fields in `buildPixieStreamRuleSpec`

File: `internal/controller/shadowtest_siphon.go`

In `buildPixieStreamRuleSpec()`, always set `ShadowNamespace` and conditionally set
`MongoOTelEndpoint`:

```go
spec := enginev1alpha1.PixieStreamRuleSpec{
    ShadowTestRef:   st.Namespace + "/" + st.Name,
    Active:          true,
    TargetNamespace: targetNamespaceFor(st),
    TargetLabels:    copyStringMap(target.Spec.Template.Labels),
    MaxPayloadSize:  siphonMaxPayloadSize(st),
    ExcludePaths:    siphonExcludePaths(st),
    ShadowNamespace: shadowNS,              // ← new: always set
}
// ... existing ingress / egress blocks ...
if hasMongoDependency(st) {
    spec.MongoOTelEndpoint = beruLocalOTLPEndpoint(shadowNS)  // ← new
}
```

Add helper next to `shadowSiphonOTelEndpoint`:

```go
func beruLocalOTLPEndpoint(shadowNS string) string {
    return fmt.Sprintf("beru-local.%s.svc.cluster.local:%d", shadowNS, 4317)
}
```

`hasMongoDependency` lives in `shadowtest_dependencies.go` (same package) so it is
accessible here without import changes.

---

## 3. MongoDB PxL template

File: `testing/scripts/manifests/pixie-bridge/configmap.yaml`

Add a third key alongside the two existing HTTP templates:

```yaml
  mongodb-export.pxl.tmpl: |
    import px

    df = px.DataFrame(table='tcp_events', start_time='-30s', end_time=px.now())

    df.namespace = df.ctx['namespace']
    df.pod       = df.ctx['pod']
    df.service   = df.ctx['service']

    # Shadow namespace only — production MongoDB is NOT captured here.
    df = df[df.namespace == '__SHADOW_NAMESPACE__']
    df = df[df.remote_port == 27017]

    # Only frames that contain a W3C traceparent written into $comment by the app.
    df = df[px.contains(df.req, 'traceparent')]

    # Derive role from pod name.
    # Shadow pods: <test-name>-control-a-<hash>, -control-b-<hash>, -candidate-<hash>
    df.pod_role = px.select(
        px.contains(df.pod, '-control-a-'), 'control-a',
        px.select(
            px.contains(df.pod, '-control-b-'), 'control-b',
            px.select(
                px.contains(df.pod, '-candidate-'), 'candidate',
                'unknown'
            )
        )
    )

    df.end_time = df.time_ + df.latency

    px.export(df, px.otel.Data(
      endpoint=px.otel.Endpoint(url='__MONGO_OTEL_ENDPOINT__', insecure=True),
      resource={
        'service.name':       df.service,
        'k8s.pod.name':       df.pod,
        'k8s.namespace.name': df.namespace,
      },
      data=[
        px.otel.trace.Span(
          name='mongodb.egress',
          start_time=df.time_,
          end_time=df.end_time,
          attributes={
            'db.system':        'mongodb',
            'db.raw_payload':   df.req,
            'shadow.pod_role':  df.pod_role,
            'shadow.test_name': '__SHADOW_TEST_NAME__',
          },
        ),
      ],
    ))
```

Token → source mapping:
| Token | Source field |
|-------|-------------|
| `__SHADOW_NAMESPACE__` | `spec.shadowNamespace` |
| `__MONGO_OTEL_ENDPOINT__` | `spec.mongoOtelEndpoint` |
| `__SHADOW_TEST_NAME__` | `spec.shadowTestRef` |

---

## 4. Bridge daemon update

### `testing/scripts/lib/pixie-bridge.sh`

Add `render_pixie_mongo_pxl()` next to `render_pixie_ingress_pxl` and
`render_pixie_egress_pxl`. Follows the exact same `sed` substitution pattern:

```bash
render_pixie_mongo_pxl() {
  local rule_json="$1" out_file="$2"
  local shadow_ns mongo_ep test_name tmpl
  shadow_ns=$(echo "$rule_json" | jq -r '.spec.shadowNamespace // ""')
  mongo_ep=$(echo  "$rule_json" | jq -r '.spec.mongoOtelEndpoint // ""')
  test_name=$(echo "$rule_json" | jq -r '.spec.shadowTestRef // ""')
  tmpl=$(kubectl get cm pixie-stream-bridge -n monarch-system \
    -o jsonpath='{.data.mongodb-export\.pxl\.tmpl}' 2>/dev/null || true)
  [[ -z "$tmpl" ]] && { echo "WARN: mongodb-export.pxl.tmpl not found" >&2; return 1; }
  echo "$tmpl" \
    | sed "s|__SHADOW_NAMESPACE__|${shadow_ns}|g" \
    | sed "s|__MONGO_OTEL_ENDPOINT__|${mongo_ep}|g" \
    | sed "s|__SHADOW_TEST_NAME__|${test_name}|g" \
    > "$out_file"
}
```

### `testing/scripts/pixie-stream-bridge.sh`

In `reconcile_rules()`, after the existing `recorder_ep` block, add:

```bash
mongo_ep=$(echo "$rule_json" | jq -r '.spec.mongoOtelEndpoint // ""')
mongo_pxl="${PIXIE_BRIDGE_STATE_DIR}/${ns}-${name}-mongo.pxl"
if [[ -n "$mongo_ep" ]]; then
  render_pixie_mongo_pxl "$rule_json" "$mongo_pxl"
  if run_pixie_export_once "$mongo_pxl"; then
    ok=1
  else
    failed="${failed:+$failed+}mongo"
  fi
else
  rm -f "$mongo_pxl"
fi
```

---

## Verification

Apply the updated ConfigMap and restart the bridge:
```bash
kubectl apply -f testing/scripts/manifests/pixie-bridge/configmap.yaml
```

Check the bridge picks up the new rule and renders the PxL file:
```bash
ls /tmp/pixie-bridge-state/default-pixie-*-mongo.pxl
# file should exist

cat /tmp/pixie-bridge-state/default-pixie-*-mongo.pxl
# should show rendered namespace, endpoint, test name (no __TOKENS__ remaining)
```

On the next bridge interval, `px run -f <mongo.pxl>` should execute without error.
beru-local receives OTLP spans but skips them (Part 3 re-enables processing).
Confirm with:
```bash
kubectl logs -n shadow-default-http-mongo-test-shadow deploy/beru-local | grep -i mongo
# "OTLP Mongo egress deprecated; use wire ingest"  ← expected until Part 3
```
