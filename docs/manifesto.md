---
type: Manifesto
title: Shadow-Diff Core Premise, Philosophy & Decision-Making Manifesto
description: The core vision, strict architectural constraints, and decision compass for the Shadow-Diff open-source project.
tags: [architecture, philosophy, decision-making, framework, zero-touch, ebpf, security]
timestamp: 2026-06-27T18:45:00Z
---

# Shadow-Diff — Core Premise & Decision-Making Manifesto

## Executive Summary
Shadow-Diff is an open-source, white-box differential testing framework built for startups and scale-ups running on Kubernetes. Its goal is to allow small, fast-moving teams to deploy changes to production with 100% confidence by running isolated shadow workloads (`control-a`, `control-b`, and `candidate`) against real traffic without adding a massive development or maintenance burden.

This document serves as the core compass for the project. Every architectural decision, feature request, and code change must be evaluated against the core principles and constraints outlined below.

---

## The Strategic Vision: Startups & Scaleups
This tool is built for engineering teams where **time and focus are the scarcest resources**. Startups do not have massive Platform Engineering or DevOps teams to manage complex infrastructure, rebuild custom telemetry containers daily, or rewrite testing scripts whenever an application updates. 

Therefore, our engineering decisions must always lean toward **centralized automation over manual coordination**. If a feature requires the user to remember a step, maintain an asset, or run an external script, that complexity must be refactored directly into our core Kubernetes control plane (`Monarch`). exepet for pixies for now.

---

## Core Product Pillars

### 1. Zero Touch (Strict Separation of Concerns)
The primary rule of Shadow-Diff is that **the application under test remains completely pristine**. 
* **The Rule**: A developer should be able to drop an existing microservice container into the shadow pipeline using a declarative YAML file without modifying a single line of business code, importing a custom tracking library, or adding configuration files to their service's repository.
* **Why this matters**: If a tool forces a startup to alter their application code or clutter their dependencies with testing-specific setups, the onboarding friction becomes too high. Local development must remain entirely decoupled from the testing pipeline.
* **Language Exclusions**: Compiled runtimes like **Go** are intentionally excluded from current support. Go's runtime behavior requires manual context propagation to pass telemetry down untracked routines, which fundamentally violates our promise of zero application code changes. We strictly support languages where bytecode/runtime auto-instrumentation is native (Node.js and Python).

### 2. Deep White-Box Auditing
We do not build a traditional black-box tester. Black-box tools only inspect final API HTTP response payloads, which inherently misses internal system degradation.
* **The Rule**: We test microservices from the inside out. We intercept internal data-layer side effects—such as exact database statements (e.g., MongoDB) and outbound asynchronous message events (e.g., RabbitMQ)—before they cross the network boundary.
* **Why this matters**: In distributed, event-driven architectures, a service might return an `HTTP 200 OK` to an ingress hub but simultaneously write corrupt data to a database or fire a broken message down an exchange. White-box auditing captures these silent architectural regressions before they hit production state.

### 3. Native Multi-Language Abstraction
Modern architectures are polyglot. The framework must treat runtime language disparities as an infrastructure problem, not an application problem.
* **The Rule**: Whether a service is written in Node.js or Python today (or Java and C# tomorrow), the system must handle it seamlessly. 
* **Why this matters**: Different languages have completely different configuration profiles. Shadow-Diff abstracts these headaches away inside the platform layer so the user receives a unified experience regardless of their tech stack.

### 4. Absolute Safety ("Do No Harm")
The first rule of shadow testing is simple: **Do no harm**. If your testing framework slows down real user requests or risks crashing production, it is an immediate non-starter.
* **The Rule**: Production capture must be entirely out-of-band and non-blocking. 
* **Why this matters**: Heavy service meshes or inline application proxies add latency, eat up precious CPU/Memory resources, and introduce a terrifying blast radius—if the proxy hangs, production hangs. 
* **Stack Isolation**: We strictly isolate proxy overhead to the shadow namespace where we can afford fine-grained proxy control. Production traffic capture is strictly restricted to kernel-level eBPF sniffing (via Pixie) for HTTP, or passive, native message duplication via broker-native routing keys for AMQP. If the capture pipeline experiences an issue, production remains completely untouched and stable.

### 5. Pragmatic Security & Data Isolation ("Guard the Data")
Because Shadow-Diff replays real production traffic, it inherently processes live data belonging to real people. Protecting this data is a strict constraint, but security must never come at the expense of developer usability.
* **The Rule**: Security boundaries are enforced natively at the infrastructure layer through complete cryptographic and network isolation, keeping the user experience frictionless.
* **Why this matters**: If a tool forces complex manual encryption keys or rigid token workflows onto a startup, they will turn it off. Instead, we use a zero-configuration isolation design:
  * **Network Sandboxing**: All shadow pods, mirrored databases, and internal analysis queues are locked down inside an isolated Kubernetes namespace using strict network policies. Real user data is captured out-of-band, replayed inside the sandbox, and can never leak back out into the public internet or production state.
  * **Strict Egress Mocking**: Envoy intercepts and strictly overrides outbound network connections. Live application containers inside the shadow test can never accidentally trigger real side effects against third-party production APIs (like Stripe or SendGrid) containing real customer information.
---

## The Decision-Making Compass

When faced with a fork in the road during development, use these verified project paradigms to decide the path forward:

### Platform Convergence over Manual Boilerplate
* **Scenario**: A component (like the OpenTelemetry Operator) requires a secondary configuration manifest (`Instrumentation` CR) to function alongside our pod injections.
* **The Wrong Path**: Expecting the user to run an installation script, maintain an external YAML file, or copy-paste standard manifests into their namespace.
* **The Shadow-Diff Path**: Merging that logic directly into our Kubernetes controller (`Monarch`). Monarch must dynamically generate and reconcile that configuration on the fly. This turns a multi-step infrastructure hurdle into an invisible platform feature.

### Clean Infrastructure Profiles over Application Hacking
* **Scenario**: A specific runtime engine (like Node.js) blocks a required telemetry feature unless we execute custom initialization code.
* **The Wrong Path**: Forcing a custom startup script into the application repository or writing a highly fragile runtime "monkey-patch" that intercepts internal library methods.
* **The Shadow-Diff Path**: Using write-time Kubernetes manifest mutation via `initContainers` and volume-mounted injection loops. By programmatically wrapping the application's boot vector with an automated platform script inside the PodSpec, we manipulate runtime execution entirely at the infrastructure layer. We force critical white-box tracing constraints—like `enhancedDatabaseReporting` for Node.js and statement captures for Python—to be **enabled natively and unconditionally by default**. This eliminates Docker registry bloat, leaves the application source code 100% untouched, and ensures our platform uses stable, public APIs that won't break on minor library updates.

### Structural Verification over String Flaking
* **Scenario**: Database statements and event payloads naturally contain non-deterministic data (timestamps, auto-generated IDs, random hashes).
* **The Rule**: Our analysis engine (`Beru`) relies on a **diff-of-diffs** model. By comparing `control-a` against `control-b`, we isolate dynamic system noise. This allows us to safely validate the structural shape and correctness of the `candidate` workload without setting up complex payload scrubbing rules.
* **Egress Sequence Logic**: For database interactions, we pair events by their semantic signature (e.g., `mongodb:insert:orders`), not their strict execution index. We track N+1 loop counts to immediately flag query loop anomalies without misaligning subsequent event tracks. All telemetry is persisted directly to an underlying SQLite database to provide clear sequence debugging via the dashboard.