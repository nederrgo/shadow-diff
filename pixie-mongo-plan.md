# Plan: Monarch Sandbox Lockdown + Pixie MongoDB Egress

## Context

Phase 2b tried to capture MongoDB wire egress via an Envoy network Lua filter.
That failed because `envoy.filters.network.lua` does not exist in any modern Envoy
image (the extension was removed). OTel auto-injection was simultaneously removed from
Monarch (correct direction), leaving `e2e-mongo-egress-test.sh` also broken.

Result: zero working MongoDB egress capture across all three tests.

This plan locks Monarch into its proper operational boundary, strips Envoy of all
Layer 4 MongoDB plumbing, and extends the **already-implemented** Pixie eBPF
infrastructure (PixieStreamRule CRD + bridge + PxL templates) to capture MongoDB
wire traffic via `tcp_events`.

---

## Step 1 — RBAC Lockdown (DevOps-managed, static manifests)

**Goal:** Monarch can read production deployments (for config copying) and manage
Pixie stream rules, but **cannot modify** production workloads. Monarch never creates
its own RoleBindings — DevOps applies the RBAC manifests directly.

**Files:** `config/rbac/role.yaml`, new `config/rbac/shadow_deployment_role.yaml`

### Changes to `manager-role` ClusterRole

`apps` deployments — change from full CRUD to **read-only**:

| Before | After |
|--------|-------|
| create, delete, get, list, patch, update, watch | **get, list, watch** |

Monarch can copy a production Deployment spec but can never create, patch, or delete
one anywhere — including production.

### New file: `config/rbac/shadow_deployment_role.yaml`

New ClusterRole `shadow-deployment-manager` with full deployment CRUD:
```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: shadow-deployment-manager
rules:
- apiGroups: ["apps"]
  resources: ["deployments"]
  verbs: ["create", "delete", "get", "list", "patch", "update", "watch"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: shadow-deployment-rolebinding
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: shadow-deployment-manager
subjects:
- kind: ServiceAccount
  name: monarch-controller-manager
  namespace: monarch-system
```

DevOps applies this alongside the existing `role.yaml`. Future: add OPA/Gatekeeper
`ConstraintTemplate` to enforce `shadow-*` namespace prefix for any Deployment write
by the Monarch SA.

### Pixie RBAC additions to `manager-role`

Confirm `""` events (create, patch) is present — controller-runtime emits events.
No other changes needed; `pixiestreamrules` CRUD is already in the role.

---

## Step 2 — Envoy HTTP-only (remove all L4 MongoDB plumbing)

**Goal:** Envoy sidecar handles only HTTP ingress + HTTP egress. MongoDB traffic
flows directly from the app to the shadow MongoDB service — no Envoy intercept.

### Files to change

**`internal/controller/shadowtest_envoy.go`**
- Delete `buildMongoEgressListenerYAML()` and its call in `renderEnvoyYAML()`
- Delete `buildMongoEgressClustersYAML()` and its call
- Delete `hasMongoDependency()`, `mongoDependency()`, `isMongoDependency()`,
  `isMongoDependencyType()` (Envoy-specific copies; deps.go has its own)
- Change `envoyImage`: `envoy-contrib:v1.30-latest` → `envoy:v1.30-latest`

**`internal/controller/shadowtest_constants.go`**
- Delete `mongoProxyPort`, `shadowMongoProxyURL`, `mongoUpstreamCluster`

**`internal/controller/shadowtest_helpers.go`**
- Remove the `"mongo-egress"` container port annotation (line 94)

**`internal/controller/shadowtest_dependencies.go`**
- Remove `usesMongoProxyInjection()` and its branch in `dependencyEnvValue()`
- MongoDB URL now falls through to the `dependencyEndpoint()` path but needs the
  `mongodb://` scheme prefix
- Add: `func mongoURL(ep string) string { return "mongodb://" + ep }`
- In `dependencyEnvValue()`: detect MongoDB by port 27017 and wrap with `mongoURL()`

**`internal/controller/shadowtest_envoy_test.go`**
- Update `TestRenderEnvoyYAML_mongoEgress`: remove `mongo_egress`, `mongo_proxy`,
  Lua checks. Verify `mongo_egress` listener is **absent** from rendered YAML.

---

## Step 3 — Extend Pixie for MongoDB egress via `tcp_events`

The existing infrastructure is fully implemented for HTTP (PixieStreamRule CRD,
`shadowtest_siphon.go` reconciler, bridge daemon, PxL templates in ConfigMap).
MongoDB is an additive extension using the same patterns.

### 3a. New fields in `PixieStreamRuleSpec`

File: `api/v1alpha1/pixiestreamrule_types.go`

```go
// MongoOTelEndpoint is the gRPC OTLP export destination for MongoDB egress spans.
// Non-empty activates the mongodb-export PxL script (tcp_events on port 27017).
// +optional
MongoOTelEndpoint string `json:"mongoOtelEndpoint,omitempty"`

// ShadowNamespace is the shadow namespace (shadow-<cr-ns>-<cr-name>) where
// shadow pods run. Used by the MongoDB PxL template to filter shadow-side traffic.
// Distinct from TargetNamespace (the production namespace used for HTTP ingress).
// +optional
ShadowNamespace string `json:"shadowNamespace,omitempty"`
```

Also update `zz_generated.deepcopy.go` (run `make generate` or hand-edit).

### 3b. Populate the new field in `buildPixieStreamRuleSpec`

File: `internal/controller/shadowtest_siphon.go`

```go
// after the egress block in buildPixieStreamRuleSpec():
if hasMongoDependency(st) {
    spec.MongoOTelEndpoint = beruLocalOTLPEndpoint(shadowNS)
}
```

Add helpers next to `shadowSiphonOTelEndpoint`:
```go
func beruLocalOTLPEndpoint(shadowNS string) string {
    return fmt.Sprintf("beru-local.%s.svc.cluster.local:%d", shadowNS, 4317)
}
```

Also set `ShadowNamespace` in the spec:
```go
spec.ShadowNamespace = shadowNS
```

Move `hasMongoDependency` (or an equivalent port-27017 check) to
`shadowtest_dependencies.go` so it's accessible from both `siphon.go` and
`envoy.go` without duplication.

### 3c. MongoDB PxL template

File: `testing/scripts/manifests/pixie-bridge/configmap.yaml` — add third key:

```yaml
  mongodb-export.pxl.tmpl: |
    import px

    df = px.DataFrame(table='tcp_events', start_time='-30s', end_time=px.now())

    df.namespace = df.ctx['namespace']
    df.pod       = df.ctx['pod']
    df.service   = df.ctx['service']

    # filter to the SHADOW namespace (not prod) and MongoDB port
    df = df[df.namespace == '__SHADOW_NAMESPACE__']
    df = df[df.remote_port == 27017]

    # only frames that contain a traceparent (written into $comment by the app)
    df = df[px.contains(df.req, 'traceparent')]

    # Derive pod_role from pod name. Shadow pods are named
    # <test-name>-control-a-<hash>, <test-name>-control-b-<hash>,
    # <test-name>-candidate-<hash>.
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
        'service.name':        df.service,
        'k8s.pod.name':        df.pod,
        'k8s.namespace.name':  df.namespace,
      },
      data=[
        px.otel.trace.Span(
          name='mongodb.egress',
          start_time=df.time_,
          end_time=df.end_time,
          attributes={
            'db.system':            'mongodb',
            'db.raw_payload':       df.req,
            'shadow.pod_role':      df.pod_role,
            'shadow.test_name':     '__SHADOW_TEST_NAME__',
          },
        ),
      ],
    ))
```

Template substitutions:
- `__SHADOW_NAMESPACE__` → `spec.shadowNamespace` ← **shadow** namespace, not prod
- `__MONGO_OTEL_ENDPOINT__` → `spec.mongoOtelEndpoint`
- `__SHADOW_TEST_NAME__` → `spec.shadowTestRef` (the `<ns>/<name>` string already on the spec)

The `shadow.pod_role` attribute carries `"control-a"`, `"control-b"`, or `"candidate"`.
The `shadow.test_name` attribute carries the owning ShadowTest name so Beru can
associate the event with the right test even when beru-local is shared.

> **Future prod MongoDB record-and-replay**: add a separate `MongoRecordOTelEndpoint`
> field + a `mongodb-prod-export.pxl.tmpl` that targets `spec.targetNamespace` (prod)
> and `spec.targetLabels` (prod pod selector) on port 27017. Pod role for prod events
> would be `"prod"` or a sentinel. This follows the exact same pattern as the existing
> `http-egress-export.pxl.tmpl` → recorder flow.

### 3d. Bridge daemon update

**`testing/scripts/lib/pixie-bridge.sh`**

Add `render_pixie_mongo_pxl(rule_json, out_file)` next to the existing
`render_pixie_ingress_pxl` and `render_pixie_egress_pxl` functions.
Substitutes four tokens via `sed`:
- `__SHADOW_NAMESPACE__` ← `jq -r '.spec.shadowNamespace'`
- `__MONGO_OTEL_ENDPOINT__` ← `jq -r '.spec.mongoOtelEndpoint'`
- `__SHADOW_TEST_NAME__` ← `jq -r '.spec.shadowTestRef'`

**`testing/scripts/pixie-stream-bridge.sh`**

In `reconcile_rules()`, after the `recorder_ep` block add:
```bash
mongo_ep=$(echo "$rule_json" | jq -r '.spec.mongoOtelEndpoint // ""')
mongo_pxl="${PIXIE_BRIDGE_STATE_DIR}/${ns}-${name}-mongo.pxl"
if [[ -n "$mongo_ep" ]]; then
  render_pixie_mongo_pxl "$rule_json" "$mongo_pxl"
  run_pixie_export_once "$mongo_pxl" || failed="${failed:+$failed+}mongo"
else
  rm -f "$mongo_pxl"
fi
```

---

## Step 4 — Re-enable OTLP MongoDB in Beru

The processing code already exists and works. It was disabled by a single skip block.

### `pipeline/beru/internal/otlp/server.go`

Remove the MongoDB skip loop in `Export()`. Route spans where `isMongoSpan(span)`
is true through `r.router.Route(FromMongoEgress(mongoHintsFromSpan(span)))`.

### `pipeline/beru/internal/otlp/mongo_parser.go`

Add `ExtractTraceparentFromRaw(raw string) string`:
```go
var traceparentRE = regexp.MustCompile(
    `00-[0-9a-f]{32}-[0-9a-f]{16}-[0-9a-f]{2}`,
)

func ExtractTraceparentFromRaw(raw string) string {
    return traceparentRE.FindString(raw)
}
```

The W3C traceparent written into MongoDB `$comment` by the app is stored as a UTF-8
string inside the BSON binary. It appears in plaintext inside the raw TCP payload
that Pixie ships as `db.raw_payload`.

### `mongoHintsFromSpan()` update

Check for `db.raw_payload` span attribute. If present, call
`ExtractTraceparentFromRaw` and populate `TraceID` in the returned `MongoHints`
so the diff engine keys on the app's trace rather than the OTel span's ID.

Also extract `shadow.pod_role` and `shadow.test_name` from span attributes and
return them alongside the hints so the `Export()` caller can pass them to
`FromMongoEgress()` as `PodRole` and `ShadowTestName` — both are required fields
on `NetworkEventEnvelope` for the diff engine to bucket events by role.

```go
// in Export(), inside the isMongoSpan branch:
hints, podRole, testName := mongoHintsFromSpan(span)
r.router.Route(FromMongoEgress(hints, podRole, testName))
```

---

## Data Flow (end state)

```
App → mongodb://shadow-mongo-svc:27017  (direct, no Envoy intercept)
                    ↓
         MongoDB shadow service
                    ↓  (wire traffic visible at kernel layer)
         Pixie eBPF  tcp_events  port=27017, namespace=shadow-*
                    ↓  px.export  OTLP/gRPC
         beru-local :4317  (OTLP gRPC receiver per shadow namespace)
                    ↓  Export() → isMongoSpan → mongoHintsFromSpan
                    ↓  ExtractTraceparentFromRaw → TraceID
         TraceRouter → diff engine
                    ↓
         "No egress regression for Trace X (mongodb)"
```

---

## Files Changed Summary

| File | Change |
|------|--------|
| `config/rbac/role.yaml` | deployments → read-only |
| `config/rbac/shadow_deployment_role.yaml` | NEW: shadow deployment CRUD |
| `internal/controller/shadowtest_envoy.go` | remove mongo L4 listener/cluster/helpers |
| `internal/controller/shadowtest_constants.go` | remove mongo constants |
| `internal/controller/shadowtest_helpers.go` | remove mongo-egress port annotation |
| `internal/controller/shadowtest_dependencies.go` | remove proxy injection, add mongoURL() |
| `internal/controller/shadowtest_envoy_test.go` | update mongo test |
| `api/v1alpha1/pixiestreamrule_types.go` | add MongoOTelEndpoint + ShadowNamespace fields |
| `api/v1alpha1/zz_generated.deepcopy.go` | regenerate |
| `internal/controller/shadowtest_siphon.go` | populate MongoOTelEndpoint |
| `testing/scripts/manifests/pixie-bridge/configmap.yaml` | add mongodb-export.pxl.tmpl |
| `testing/scripts/lib/pixie-bridge.sh` | add render_pixie_mongo_pxl() |
| `testing/scripts/pixie-stream-bridge.sh` | add mongo PxL execution |
| `pipeline/beru/internal/otlp/server.go` | re-enable MongoDB OTLP routing |
| `pipeline/beru/internal/otlp/mongo_parser.go` | add ExtractTraceparentFromRaw() |
| `testing/scripts/e2e-http-mongo-test.sh` | re-add MongoDB Beru assertion |
| `testing/scripts/e2e-rmq-mongo-test.sh` | re-add MongoDB Beru assertion |

---

## Verification

1. `go test ./pipeline/monarch/internal/controller/...`
   — `TestRenderEnvoyYAML_mongoEgress` passes (no mongo_egress listener in YAML)

2. `go test ./pipeline/beru/internal/otlp/...`
   — MongoDB span no longer skipped; routes to TraceRouter

3. E2E on Pixie-capable cluster (Minikube + kvm2):
   - `SKIP_BUILD=1 ./testing/scripts/e2e-http-mongo-test.sh`
   - `SKIP_BUILD=1 ./testing/scripts/e2e-rmq-mongo-test.sh`
   - `SKIP_BUILD=1 ./testing/scripts/e2e-mongo-egress-test.sh`
