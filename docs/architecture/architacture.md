# Shadow-Diff Architecture: Monarch & Beru

## 1. Overview
Shadow-Diff is an open-source, generic differential testing framework for Kubernetes. It captures real production traffic and replays it across three isolated environments to identify regressions while filtering out non-deterministic noise (timestamps, UUIDs).

### The Three-Pod Strategy
- **Control-A (Old Version):** Baseline for comparison.
- **Control-B (Old Version):** Identical to A; used to identify dynamic/noise fields.
- **Candidate (New Version):** The version being tested.

---

## 2. Core Components

### A. Monarch (The Orchestrator)
A Kubernetes Operator built with KubeBuilder.
- **Responsibility:** Manages the lifecycle of a `ShadowEnvironment` Custom Resource (CRD).
- **Functions:**
    - Clones existing production Deployments.
    - Injects Sidecars for egress redirection (Mocking DBs/Brokers).
    - Provisions "Shadow Dependencies" (ephemeral DBs) if required.
    - Deploys the **Siphon Agent** to the production pod.

### B. The Siphon Agent (Traffic Capture)
A lightweight eBPF-based agent.
- **Responsibility:** Passive capture of inbound traffic.
- **Functions:**
    - Uses eBPF (Socket Filter/TC) to clone incoming TCP/HTTP packets.
    - Forwards packets to Monarch's internal router, which multicasts them to the 3 Shadow pods.
    - **Security:** Redacts sensitive PII at the kernel level before egress.

### C. Beru (The Differ)
The data processing and analytics engine.
- **Responsibility:** Multi-way comparison of responses.
- **Logic (The "Diff-of-Diffs"):**
    1. **Diff(A, B):** Identifies "Noise" (e.g., fields that change between two identical runs).
    2. **Diff(A, Candidate):** Identifies "Total Changes."
    3. **Result:** `Total Changes - Noise = Regressions`.
- **Storage:** Uses Redis for short-term message buffering and PostgreSQL/Clickhouse for long-term reporting.

---

## 3. Data Flow

1. **Capture:** Siphon Agent captures a request in Prod.
2. **Multicast:** The request is sent to Control-A, Control-B, and Candidate.
3. **Execution:**
    - Shadow pods process the request.
    - Egress calls (DB/API) are intercepted by an **Envoy Sidecar** and redirected to shadow instances or mocked.
4. **Collection:** Sidecars capture the *responses* and send them to **Beru**.
5. **Analysis:** Beru performs the 3-way diff and updates the dashboard.

---

## 4. Technology Stack
- **Control Plane:** Go, KubeBuilder (Kubernetes Operator SDK).
- **Data Plane (Capture):** eBPF (cilium/ebpf), Go.
- **Proxy/Sandbox:** Envoy (Sidecar) with `ext_proc`.
- **Processing:** Go, gRPC.
- **State:** Redis (Matching), PostgreSQL (Results).