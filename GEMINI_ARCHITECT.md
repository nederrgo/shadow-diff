Here is a comprehensive Markdown document outlining the future architecture of the project. You can save this as `ARCHITECTURE_V2.md` or `FUTURE_ARCHITECTURE.md` in your repository. 

***

# Monarch — Future Architecture & Vision

This document describes the target architecture for the **Monarch** shadow testing ecosystem. 

Moving beyond basic Kubernetes workload provisioning, Monarch is evolving into a **modular, cloud-native traffic analysis platform**. It aims to securely mirror, replay, and diff traffic for any protocol (HTTP, gRPC, Redis, PostgreSQL, etc.) by decoupling Kubernetes orchestration, high-performance packet capture, traffic routing, and noise-canceling diff logic.

---

## High-Level System Context

The ecosystem is divided into four primary components:
1.  **Monarch:** The Control Plane (Kubernetes Operator).
2.  **The Tapper:** The eBPF Data Plane (High-performance L4 mirroring).
3.  **Igris:** The Traffic Router & Protocol Framer.
4.  **Beru:** The Diffing & Analysis Engine.

```mermaid
flowchart TD
  subgraph Production [Production Namespace]
    Tgt[Target Pod\nIP: 10.0.0.5]
  end

  subgraph Node [Kubernetes Node Kernel]
    eBPF[eBPF Tapper\nL3/L4 Filtering]
    eBPF_Map[(eBPF IP/Port Map)]
  end

  subgraph Shadow [Shadow Namespace]
    Igris[Igris Router\nProtocol Framer]
    Beru[Beru Analyzer\nNoise Canceler]
    
    A[Control A]
    B[Control B]
    C[Candidate]
  end

  subgraph ControlPlane [Control Plane]
    Monarch[Monarch Operator]
  end

  %% Control Plane Flow
  Monarch -.->|Watches Target Pod IPs\nUpdates Map| eBPF_Map
  Monarch -.->|Provisions & Mounts Configs| Shadow
  
  %% Data Plane Flow
  UserClient -->|Real Traffic| Tgt
  Tgt -.->|tc/kprobe| eBPF
  eBPF_Map -.->|Filter Check| eBPF
  
  eBPF == RingBuffer ==> Igris
  Igris == Frames Request ==> A & B & C
  A & B & C == Responses ==> Beru
  Igris -.->|Passes Expected Protocol| Beru
```

---

## Component Deep Dive

### 1. Monarch (The Orchestrator)
Monarch remains the Kubernetes Operator (`controller-runtime`), but its responsibilities are strictly focused on **infrastructure, configuration, and security**.

*   **State Synchronization:** Deep-copies the Target Deployment's configurations (including ConfigMaps and Secrets) to ensure shadow pods can boot.
*   **eBPF Management:** Watches the K8s API for target Pod IP changes (scale up/down, rollouts) and continuously updates the shared **eBPF Maps** so the Tapper knows which traffic to mirror.
*   **Security Enforcement:** Injects strict `NetworkPolicies` into the shadow namespace. Egress is blackholed (except to `Igris`/`Beru`) to guarantee shadow deployments cannot mutate production databases or external APIs.

### 2. The Tapper (eBPF Data Plane)
To support "all things TCP" without crushing node CPU, traffic mirroring is handled in the Linux kernel via eBPF.

*   **Zero L7 Logic:** The Tapper is completely protocol-agnostic. It does not parse HTTP or read headers.
*   **L4 Filtering:** It intercepts raw network packets and checks the Destination IP and Port against the eBPF Map provided by Monarch. If there is a match, it clones the raw TCP stream and pushes it to a user-space RingBuffer.

### 3. Igris (The Router & Framer)
Igris bridges the raw TCP stream to the application layer. It receives the raw byte stream from the eBPF Tapper.

*   **Protocol Framing:** Raw TCP cannot be blindly copied to 3 destinations. Igris uses the **Plugin Architecture** to know where a request starts and ends (e.g., finding `\r\n\r\n` for HTTP, or parsing RESP arrays for Redis).
*   **L7 Filtering:** Drops requests that the user explicitly wants to ignore (e.g., dropping `/healthz` endpoints, or dropping all `POST` requests to prevent state mutation in the shadow env).
*   **Replay:** Opens isolated TCP connections to Control A, Control B, and Candidate, multiplexing the framed request to all three simultaneously.

### 4. Beru (The Differ & Analyzer)
Beru is the intelligence layer. It receives the responses from A, B, and the Candidate via Igris.

*   **Noise Cancellation (Normalization):** Uses the Plugin Architecture to strip dynamic, non-deterministic data (e.g., `Date` headers, UUIDs, auto-incrementing DB IDs, timestamps).
*   **A/B/C Diffing Algorithm:** Compares Control A to Control B to establish a baseline of "acceptable variance" (noise). It then compares Candidate to the baseline. If Candidate differs in a way A and B did not, it flags a **Regression**.

---

## The Modularity Strategy (Plugin Ecosystem)

To support new technologies without rewriting the core system, **Igris and Beru are completely modular.** They utilize a unified interface (via HashiCorp `go-plugin` or WASM).

When a user defines `spec.protocol: "redis"` in the `ShadowTest` CRD, Monarch loads the Redis Add-on into Igris and Beru.

### The `ProtocolPlugin` Interface

Anyone in the open-source community can write an add-on by implementing three distinct functions:

```go
type ProtocolPlugin interface {
    // 1. Framer (Used by Igris)
    // Chunks the raw TCP stream from eBPF into individual, complete requests.
    ExtractMessage(rawStream []byte) (messages [][]byte, bytesConsumed int)

    // 2. Normalizer (Used by Beru)
    // Strips out timestamps, random IDs, or volatile headers before diffing.
    Normalize(response []byte) (normalizedResponse []byte)

    // 3. Differ (Used by Beru)
    // Compares normalized payloads (e.g., JSON structure, SQL rows).
    Diff(controlA []byte, controlB []byte, candidate []byte) DiffResult
}
```

### Supported Technologies (Roadmap)
*   **MVP:** HTTP/1.1 (REST/JSON)
*   **Phase 2:** gRPC / HTTP2
*   **Phase 3 (Community Add-ons):** Redis, PostgreSQL, Kafka, raw TCP wrappers.

---

## Data Flow: Lifecycle of a Request

1.  **Map Update:** Monarch detects target Pod `10.0.0.5:8080` and writes it to the eBPF Map.
2.  **Capture:** A client sends an HTTP `GET /users` request to the target pod. The eBPF Tapper sees `10.0.0.5:8080`, clones the packets, and sends them to the RingBuffer.
3.  **Framing:** Igris pulls the bytes, uses the HTTP plugin's `ExtractMessage` function to reconstruct the full HTTP request, and determines it is safe to replay.
4.  **Routing:** Igris sends the cloned request to Control A, Control B, and Candidate.
5.  **Response:** The three pods process the request and return HTTP responses.
6.  **Normalization:** Beru receives the responses, uses the HTTP plugin to strip out the `Date: ...` and `X-Request-Id: ...` headers.
7.  **Analysis:** Beru compares the bodies. If A and B match, but Candidate returns a 500 or missing JSON field, Beru logs a diff failure and exposes the metric to Prometheus.

---

## Security & State Safety

Shadow testing carries the inherent risk of mutating production state (e.g., sending duplicate purchase requests or writing to a production database). This architecture mitigates this via a **defense-in-depth** approach:

1.  **L7 Dropping (Igris):** Add-ons can be configured to drop mutating requests entirely (e.g., drop `POST/PUT/DELETE` or drop SQL `INSERT/UPDATE`).
2.  **Egress Blackholing (Monarch):** Monarch strictly enforces a Kubernetes `NetworkPolicy` in the shadow namespace. Even if a mutating request gets through Igris, the shadow pods literally cannot route traffic to external databases or APIs. They will safely fail or timeout, preventing catastrophic production corruption.
