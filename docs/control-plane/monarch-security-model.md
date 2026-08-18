---
type: Architecture Specification
title: Monarch Control Plane Security Model
description: Deep dive into the least-privilege boundaries, RBAC constraints, and workload isolation mechanics governing the Monarch controller.
resource: https://github.com/your-org/shadow-diff/tree/main/pipeline/monarch
tags: [architecture, security, monarch, kubernetes, sandboxing]
timestamp: 2026-08-18T18:40:00Z
---

# Monarch Control Plane Security Model

## Executive Summary
Because `Monarch` serves as the centralized orchestration control plane for Shadow-Diff, it requires specific privileges to handle workload mutations, namespace configurations, and ephemeral resource lifecycle coordination. To safeguard the host cluster, Monarch acts under a strict **zero-trust execution posture**. 

Crucially, Monarch avoids high-privilege cluster operations: it does **not** manage cluster-wide kernel tracing elements or inject raw eBPF drivers natively. It delegates cluster-level kernel observation out-of-band via decoupled, custom resource boundaries.

---

## 1. Cluster RBAC & Least-Privilege Restrictions
Monarch avoids a blanket `ClusterAdmin` role. Instead, its access is compartmentalized to limit the radius of potential exploitation.

### RBAC Permission Boundaries
* **Cluster-wide (manager-role)**: Namespace lifecycle (`create`/`delete` on `namespaces` — **enforced to `shadow-*` only** by `ValidatingAdmissionPolicy/namespace-guard`; not expressible as a native RBAC name prefix), ShadowTest/KaiselRule CRD reconciliation, read-only discovery of target `Deployments`/`ReplicaSets`/`Pods`, read-only `get`/`list`/`watch` on `ConfigMaps`/`Services` (controller-runtime informer cache; **no Secrets**, and **no create/update/delete** on those types outside shadow namespaces), `RoleBinding` create plus `bind` on ClusterRole `shadow-workload-role` only (so Monarch can grant itself shadow writes without holding those verbs cluster-wide), and Kubernetes `Events`. The same admission policy also denies RoleBinding writes outside `shadow-*`.
* **Secret source read**: The Secret informer is disabled (`Cache.DisableFor` Secrets). `BERU_DB_SECRET` is a namespaced Role `get` with `resourceNames` in the Secret's namespace. `storage.credentialsSecretRef` is a `get`-only ClusterRole (`secret-source-reader`) bound at install time into listed CR namespaces (Helm `monarch.secretSourceNamespaces`, default `default`; Kustomize e2e applies `RoleBinding/monarch-secret-source` in `default`). The manager SA does **not** hold `bind` on `secret-source-reader`. A ShadowTest in an unbound namespace fails at secret sync.
* **Shadow namespace only (shadow-workload-role)**: Full `create`/`update`/`patch`/`delete` on `ConfigMaps`, `Secrets`, `Services`, and `Deployments` **only where a per-namespace `RoleBinding` exists**. Monarch reconciles `RoleBinding/monarch-shadow-workload` into each `shadow-<crNamespace>-<crName>` namespace at boot; Kubernetes scopes the bound ClusterRole to that namespace.
* **Production / target namespace**: Read-only `get`/`list`/`watch` on the target Deployment and its Pods (for `KaiselRule` IP discovery). No writes to prod `ConfigMaps`, `Services`, or `Secrets`. No cluster-wide Secret read.
* **Secret write path**: Production application Secrets are never mutated. Monarch copies only explicitly referenced creds (`storage.credentialsSecretRef`, `BERU_DB_SECRET`) into the isolated shadow namespace.

---

## 2. High-Privilege Isolation: eBPF & Kernel Boundaries
A primary security constraint of Shadow-Diff is that the central operator must never require root kernel manipulation access. 

┌────────────────────────────────┐
│      monarch-system NS         │
│  [ Monarch Operator ]          │ ──► restricted PSA, no Linux capabilities
└──────────────┬─────────────────┘
│
│ Writes Low-Privilege CRD Manifest
▼
┌────────────────────────────────┐
│       Production / Shadow NS   │
│  [ KaiselRule CR ]             │
└──────────────┬─────────────────┘
│
│ Read out-of-band by the Kaisel DaemonSet
▼
┌────────────────────────────────┐
│         K8s Host Node          │
│  [ kaisel-system DaemonSet ]   │ ──► CAP_BPF + CAP_NET_RAW + CAP_PERFMON
└────────────────────────────────┘     (Completely isolated from Monarch)


### Decoupled eBPF Architecture
* **Strict Operator Segregation**: Monarch **does not deploy or manage the eBPF capture DaemonSet**. Because kernel capture needs `CAP_BPF`, `CAP_NET_RAW` and `CAP_PERFMON` under a `privileged` Pod Security Standard, installation and management are externalized to the platform team. Monarch itself runs under the `restricted` standard with no Linux capabilities.
* **The Declarative Interface (`KaiselRule`)**: Instead of handling eBPF logic inline, Monarch outputs a completely unprivileged custom manifest called a `KaiselRule`. This carries metadata only — live target pod IPs, TCP ports, and the igris/Shop URLs to forward to.
* **The Capture DaemonSet (`kaisel`)**: An out-of-band DaemonSet in `kaisel-system` consumes this manifest with `get/list/watch` on `kaiselrules` and nothing else. If kernel instrumentation malfunctions, Monarch's control-plane loop remains insulated — and because capture attaches a socket filter that receives a *clone* of each frame, it cannot drop or delay production traffic either. See [/data-plane/kaisel-ebpf.md](/data-plane/kaisel-ebpf.md).

---

## 3. Namespace Sandboxing & Sidecar Security
Whenever a `ShadowTest` resource is initialized, Monarch programmatically instantiates an immutable, multi-layered isolation barrier surrounding the resulting shadow namespace.

### Container Security Principles
* **No Privilege Escalation**: Injected Envoy proxy sidecars and dependency stacks (e.g., automated Redis or Mongo test stores) run strictly within unprivileged contexts (`allowPrivilegeEscalation: false`, `runAsNonRoot: true`).
* **Automated Sandbox Network Policies**: Monarch instantiates strict `NetworkPolicies` dropping all incoming ingress traffic except for out-of-band telemetry pipelines (Kaisel → igris-http and Kaisel → Shop).
* **Egress Traffic Interception**: Monarch injects an Envoy proxy configured with forceful egress filtering rules. Any attempt by a shadow component (`candidate` or `control`) to call external production APIs (e.g., Stripe, SendGrid) is trapped, cut off, and directed to mock stubs.

---

## 4. Threat Modeling Matrix

| Identified Threat | Target Surface | Monarch Mitigation Strategy |
| :--- | :--- | :--- |
| **Candidate Escape** | Production DB / APIs | Egress network policies explicitly drop all traffic trying to cross the boundary into production namespaces or outbound public IP addresses. |
| **Webhook Vulnerability** | K8s API Server | The Mutating Admission Webhook restricts mutations strictly to resources carrying the explicit `shadow-diff.io/inject: enabled` label specification. |
| **Kernel Crash / Vulnerability** | Node Kernel / eBPF | High-privilege instrumentation is decoupled. Monarch only outputs metadata (`KaiselRule`); it has zero direct connectivity to kernel trace hooks. |
| **Credential Exposure** | Production Secrets | Monarch isolates shadow pods by stripping the default `ServiceAccount` tokens from the target containers inside the shadow namespace, preventing pods from querying the cluster API server. |

---

## # Citations
* [Kubernetes Least Privilege RBAC Guide](https://kubernetes.io/docs/concepts/security/rbac-good-practices/)
* [Envoy Proxy Egress Filter Chain Configuration](https://www.envoyproxy.io/docs/envoy/latest/intro/arch_overview/advanced/matching/matching_api)