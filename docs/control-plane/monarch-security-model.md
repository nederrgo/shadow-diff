---
type: Architecture Specification
title: Monarch Control Plane Security Model
description: Deep dive into the least-privilege boundaries, RBAC constraints, and workload isolation mechanics governing the Monarch controller.
resource: https://github.com/your-org/shadow-diff/tree/main/pipeline/monarch
tags: [architecture, security, monarch, kubernetes, sandboxing]
timestamp: 2026-06-27T19:30:00Z
---

# Monarch Control Plane Security Model

## Executive Summary
Because `Monarch` serves as the centralized orchestration control plane for Shadow-Diff, it requires specific privileges to handle workload mutations, namespace configurations, and ephemeral resource lifecycle coordination. To safeguard the host cluster, Monarch acts under a strict **zero-trust execution posture**. 

Crucially, Monarch avoids high-privilege cluster operations: it does **not** manage cluster-wide kernel tracing elements or inject raw eBPF drivers natively. It delegates cluster-level kernel observation out-of-band via decoupled, custom resource boundaries.

---

## 1. Cluster RBAC & Least-Privilege Restrictions
Monarch avoids a blanket `ClusterAdmin` role. Instead, its access is compartmentalized to limit the radius of potential exploitation.

### RBAC Permission Boundaries
* **Workload Mutation (Cluster-Wide)**: Monarch is granted `get`, `list`, `watch`, and `patch` operations over standard Kubernetes workloads (`Deployments`, `StatefulSets`) strictly to process matching injection selectors via its Mutating Admission Webhook.
* **Control Plane Discovery**: Reads access permissions for core Kubernetes service objects and infrastructure mapping to discover production target workloads.
* **Shadow Boundary (Namespace-Scoped)**: Monarch acts with full operational permissions (`apps/*`, `core/*`) **exclusively inside designated shadow namespaces** (`shadow-<crNamespace>-<crName>`). It cannot write, edit, or delete non-shadow deployments.
* **Secret Restrictions**: Monarch **cannot read production application secrets**. If a shadow pod requires configuration tokens, a synthetic/mock secret placeholder is generated dynamically within the shadow space.

---

## 2. High-Privilege Isolation: eBPF & Kernel Boundaries
A primary security constraint of Shadow-Diff is that the central operator must never require root kernel manipulation access. 

┌────────────────────────────────┐
│      monarch-system NS         │
│  [ Monarch Operator ]          │
└──────────────┬─────────────────┘
│
│ Writes Low-Privilege CRD Manifest
▼
┌────────────────────────────────┐
│       Production / Shadow NS   │
│  [ PixieStreamRule CR ]        │
└──────────────┬─────────────────┘
│
│ Read out-of-band by host process
▼
┌────────────────────────────────┐
│         K8s Host Node          │
│  [ Pixie Vizier / Bridge ]     │ ──► Requires Host Kernel Access (SYS_ADMIN)
└────────────────────────────────┘     (Completely isolated from Monarch)


### Decoupled eBPF Architecture
* **Strict Operator Segregation**: Monarch **does not deploy or manage Pixie Vizier or eBPF kernel tracing sensors**. Because eBPF operators require advanced cluster privileges (such as running in the host pid namespace with `CAP_SYS_ADMIN`), installation and management are completely externalized to the platform team.
* **The Declarative Interface (`PixieStreamRule`)**: Instead of handling eBPF logic inline, Monarch outputs a completely unprivileged custom manifest called a `PixieStreamRule`. This contains metadata instructions (production target pod labels, shadow ports, and OTLP endpoints).
* **The Bridge Gateway**: An external, out-of-band component (`pixie-stream-bridge`) running as an independent host daemon consumes this manifest. It executes the high-privilege PxL scripts required to mirror the traffic. If the bridge or kernel instrumentation malfunctions, Monarch's control plane loop remains completely insulated and untouched.

---

## 3. Namespace Sandboxing & Sidecar Security
Whenever a `ShadowTest` resource is initialized, Monarch programmatically instantiates an immutable, multi-layered isolation barrier surrounding the resulting shadow namespace.

### Container Security Principles
* **No Privilege Escalation**: Injected Envoy proxy sidecars and dependency stacks (e.g., automated Redis or Mongo test stores) run strictly within unprivileged contexts (`allowPrivilegeEscalation: false`, `runAsNonRoot: true`).
* **Automated Sandbox Network Policies**: Monarch instantiates strict `NetworkPolicies` dropping all incoming ingress traffic except for out-of-band telemetry pipelines authorized through the `Siphon` gateway service (`:4317`).
* **Egress Traffic Interception**: Monarch injects an Envoy proxy configured with forceful egress filtering rules. Any attempt by a shadow component (`candidate` or `control`) to call external production APIs (e.g., Stripe, SendGrid) is trapped, cut off, and directed to mock stubs.

---

## 4. Threat Modeling Matrix

| Identified Threat | Target Surface | Monarch Mitigation Strategy |
| :--- | :--- | :--- |
| **Candidate Escape** | Production DB / APIs | Egress network policies explicitly drop all traffic trying to cross the boundary into production namespaces or outbound public IP addresses. |
| **Webhook Vulnerability** | K8s API Server | The Mutating Admission Webhook restricts mutations strictly to resources carrying the explicit `shadow-diff.io/inject: enabled` label specification. |
| **Kernel Crash / Vulnerability** | Node Kernel / eBPF | High-privilege instrumentation is decoupled. Monarch only outputs metadata (`PixieStreamRule`); it has zero direct connectivity to kernel trace hooks. |
| **Credential Exposure** | Production Secrets | Monarch isolates shadow pods by stripping the default `ServiceAccount` tokens from the target containers inside the shadow namespace, preventing pods from querying the cluster API server. |

---

## # Citations
* [Kubernetes Least Privilege RBAC Guide](https://kubernetes.io/docs/concepts/security/rbac-good-practices/)
* [Envoy Proxy Egress Filter Chain Configuration](https://www.envoyproxy.io/docs/envoy/latest/intro/arch_overview/advanced/matching/matching_api)
* [Pixie eBPF Security Architecture & Memory Isolation Model](https://docs.px.dev/about/architecture/)