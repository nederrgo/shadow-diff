# Part 1 — Cleanup: RBAC Lockdown + Envoy HTTP-only

## Goal
Stop the crashing Envoy pods. Strip all L4 MongoDB plumbing from Envoy so it
handles only HTTP. Fix MongoDB URL injection so apps connect directly to the shadow
MongoDB service. Tighten Monarch's RBAC so it cannot modify production Deployments.

No new features in this part. When it is done the shadow pods start healthy and all
existing HTTP tests pass. MongoDB egress diffing is not yet wired — that is Part 2/3.

---

## 1. RBAC (DevOps applies, no controller code changes)

### `config/rbac/role.yaml`

Change `apps` / `deployments` verbs in `manager-role`:

```
Before: create, delete, get, list, patch, update, watch
After:  get, list, watch
```

### New file: `config/rbac/shadow_deployment_role.yaml`

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

DevOps applies both files at deploy time. Monarch SA ends up with:
- `manager-role` — read-only on Deployments everywhere (for prod config copy)
- `shadow-deployment-manager` — full Deployment CRUD (controller only writes to shadow namespaces)

---

## 2. Envoy — remove all L4 MongoDB plumbing

### `internal/controller/shadowtest_constants.go`

Delete these constants (they will have no callers after the envoy.go changes):
```go
mongoProxyPort       int32  = 27017
shadowMongoProxyURL  string = "mongodb://127.0.0.1:27017"
mongoUpstreamCluster        = "mongo_upstream"
```

### `internal/controller/shadowtest_envoy.go`

Delete functions (and their call sites in `renderEnvoyYAML`):
- `buildMongoEgressListenerYAML()`
- `buildMongoEgressClustersYAML()`
- `hasMongoDependency()` — Envoy-specific copy; leave the one in dependencies.go
- `mongoDependency()`
- `isMongoDependency()` — Envoy-specific copy
- `isMongoDependencyType()`

Change image constant:
```go
envoyImage = "envoy:v1.30-latest"   // was envoy-contrib:v1.30-latest
```

### `internal/controller/shadowtest_helpers.go`

Remove the `"mongo-egress"` container port from the pod template (line ~94):
```go
// delete this block:
{Name: "mongo-egress", ContainerPort: mongoProxyPort, Protocol: corev1.ProtocolTCP},
```

### `internal/controller/shadowtest_dependencies.go`

Remove `usesMongoProxyInjection()` and its branch in `dependencyEnvValue()`:

```go
// Before:
func dependencyEnvValue(shadowNS string, dep enginev1alpha1.DependencySpec, role string) string {
    _, port := resolveDependencyDefaults(dep)
    if usesMongoProxyInjection(dep) {
        return shadowMongoProxyURL          // was returning 127.0.0.1:27017
    }
    ...
}

// After:
func dependencyEnvValue(shadowNS string, dep enginev1alpha1.DependencySpec, role string) string {
    _, port := resolveDependencyDefaults(dep)
    if isMongoDependency(dep) {
        return "mongodb://" + dependencyEndpoint(shadowNS, dep.Name, role, port)
    }
    ...
}
```

Apps now get `MONGO_URL=mongodb://mongodb-control-a.<shadow-ns>.svc.cluster.local:27017`
— the actual shadow MongoDB service, no Envoy intercept.

---

## 3. Unit test update

### `internal/controller/shadowtest_envoy_test.go`

Update `TestRenderEnvoyYAML_mongoEgress`:
- Remove checks for `mongo_egress`, `envoy.filters.network.mongo_proxy`,
  `emit_dynamic_metadata`, `tcp_proxy` on port 27017, `mongo_upstream`
- Add assertion that `mongo_egress` is **absent** from the rendered YAML
- Keep checks that `MONGO_URL` env is set to the direct service URL

---

## Verification

```bash
go test ./pipeline/monarch/internal/controller/...
```

Then in the cluster:
```bash
kubectl get pods -n shadow-default-http-mongo-test-shadow
# All pods Running (no CrashLoopBackOff)

kubectl get cm http-mongo-test-shadow-control-a-envoy -n shadow-default-http-mongo-test-shadow \
  -o jsonpath='{.data.envoy\.yaml}' | grep mongo_egress
# (empty — listener is gone)
```
