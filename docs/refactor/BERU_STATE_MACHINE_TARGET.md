Here is the complete, updated master blueprint containing the signature-based pairing algorithm guardrails. Save this text as docs/refactor/BERU_STATE_MACHINE_TARGET.md in your repository so Cursor can read the whole picture before you start prompting for individual steps.

Markdown
# Target Architecture: Beru Row-Level State Machine

## 1. Architectural Vision
The goal of this refactor is to replace Beru’s volatile in-memory trace correlation buffers (`pt.diffDone`, `pt.ingest`, `pt.egressdiff`) with a resilient, event-driven, **Row-Level Upsert State Machine**. 

Instead of waiting for a single, immutable snapshot via network timers (`BERU_EGRESS_WAIT`) and freezing a verdict, the database (SQLite in WAL mode) becomes the live source of truth. Every inbound event across any transport protocol is saved to the database immediately. Its arrival triggers a complete timeline re-evaluation for that `trace_id`, dynamically catching late-arriving messages and N+1 regressions.

[Ingress / Egress Sources]
(HTTP ext_proc, OTLP Spans, RabbitMQ Relay, Future Kafka)
│
▼
┌──────────────────────────────┐
│  TraceRouter Shard Channel   │  ◄── Prevents database write-locks by
└───────────┬──────────────────┘      sequencing updates per TraceID in-memory
│
▼
┌──────────────────────────────┐
│  repo.AppendReport(report)   │  ◄── Writes raw row to SQLite
└───────────┬──────────────────┘
│
▼
┌──────────────────────────────┐
│   Returns []RawReport        │  ◄── Pulls ALL historical events for this trace
└───────────┬──────────────────┘
│
▼
┌──────────────────────────────┐
│ diff.EvaluateTraceHistory()  │  ◄── Computes fresh diff verdict across timeline
└───────────┬──────────────────┘
│
▼
┌──────────────────────────────┐
│ repo.SaveDiffVerdict(state)  │  ◄── Overwrites status (MATCH/MISMATCH) in DB
└──────────────────────────────┘


---

## 2. Unifying the Data Pipeline

No matter the network transport layer, all handlers normalize incoming data into a single type before pushing it to the state machine engine.

### Data Source Flow
1. **HTTP Ingress (Envoy `ext_proc`)**: Extracts trace variables, formats payload → `RawReport{Protocol: "http", Direction: "ingress"}`
2. **MongoDB Egress (OTel Agent)**: Parses `db.statement` span strings → `RawReport{Protocol: "mongodb", Direction: "egress"}`
3. **RabbitMQ Egress (relay-rabbitmq)**: Captures AMQP publish firehose frames → `RawReport{Protocol: "rabbitmq", Direction: "egress"}`
4. **Future Extensions (Kafka Streams)**: Listens to event topics → `RawReport{Protocol: "kafka", Direction: "egress"}`

---

## 3. Signature-Based Pairing Logic (Anti-Alignment Shift)

To prevent false mismatches caused by asynchronous, out-of-order execution across protocols, the evaluation engine MUST NOT use naive index-by-index comparison on the raw history array. It must use **Signature-Based Pairing**:

1. **Protocol & Role Grouping**: Extract the `[]RawReport` for a trace and filter by protocol, then group items into isolated lists for `control-a`, `control-b`, and `candidate`.
2. **Signature Bucketing**: Within each protocol group, bucket the reports into map arrays keyed by their operational `Signature` (e.g., `mongodb:insert:orders` or `rabbitmq:publish:order.created`).
3. **Intra-Bucket Diffing**: Compare the buckets across roles strictly within identical signatures. Pair index `0` of `control-a`'s signature bucket with index `0` of `candidate`'s signature bucket. This protects the alignment if a MongoDB query and a RabbitMQ publish swap places in transit.
4. **N+1 / Extra Step Detection**: If the array length of `candidate`'s bucket for a specific signature exceeds `control-a`'s length, flag an immediate `CountRegression` (N+1 anomaly) for that specific signature, while allowing older/other indexes to match clean.

---

## 4. What the Ending Code Structurally Looks Like

Below is the targeted code design that Cursor must achieve by the end of the refactor plan.

### Core Domain Types (`internal/storage/models.go`)
```go
package storage

import "time"

type PayloadDirection string
const (
    DirectionIngress PayloadDirection = "ingress"
    DirectionEgress  PayloadDirection = "egress"
)

type RawReport struct {
    TraceID      string           `json:"trace_id"`
    ShadowRole   string           `json:"shadow_role"` // control-a, control-b, candidate
    Protocol     string           `json:"protocol"`    // http, mongodb, rabbitmq, kafka
    Direction    PayloadDirection `json:"direction"`
    Signature    string           `json:"signature"`   // e.g., "mongodb:insert:orders"
    PayloadBytes []byte           `json:"payload_bytes"`
    CapturedAt   time.Time        `json:"captured_at"`
}

type VerdictState struct {
    Status             string    // MATCH, MISMATCH, TIMEOUT
    HasCountRegression bool      // Explicit flag for N+1 anomalies
    SummaryDetails     string    // JSON structure outlining path errors
    UpdatedAt          time.Time
}
The Storage Contract (internal/storage/repository.go)
Go
package storage

import "context"

type TraceRepository interface {
    // AppendReport writes a report to the database and pulls down the complete 
    // timeline history matching this TraceID for a full re-diff evaluation.
    AppendReport(ctx context.Context, report *RawReport) ([]RawReport, error)
    
    // SaveDiffVerdict upserts or updates the matching status and error metadata tables.
    SaveDiffVerdict(ctx context.Context, traceID string, verdict *VerdictState) error
}
The Execution Engine (internal/engine/router.go)
Go
package engine

import (
    "context"
    "hash/fnv"
    "log"
    "time"
    
    "your-repo/internal/diff"
    "your-repo/internal/storage"
)

type TraceRouter struct {
    workers []chan *storage.RawReport
    repo    storage.TraceRepository
}

func NewTraceRouter(workerCount int, repo storage.TraceRepository) *TraceRouter {
    tr := &TraceRouter{
        workers: make([]chan *storage.RawReport, workerCount),
        repo:    repo,
    }
    for i := 0; i < workerCount; i++ {
        tr.workers[i] = make(chan *storage.RawReport, 2048)
        go tr.startWorker(tr.workers[i])
    }
    return tr
}

// Route hashes a TraceID to forward all related protocol frames to a single sequential worker thread
func (tr *TraceRouter) Route(report *storage.RawReport) {
    hasher := fnv.New32a()
    hasher.Write([]byte(report.TraceID))
    workerIdx := hasher.Sum32() % uint32(len(tr.workers))
    tr.workers[workerIdx] <- report
}

func (tr *TraceRouter) startWorker(ch chan *storage.RawReport) {
    for report := range ch {
        ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
        
        // Step 1: Commit data to row-storage and acquire full history context
        history, err := tr.repo.AppendReport(ctx, report)
        if err != nil {
            log.Printf("[Engine] Database append fault for trace %s: %v", report.TraceID, err)
            cancel()
            continue
        }
        
        // Step 2: Re-run matching algorithms on the comprehensive timeline history array
        verdict := diff.EvaluateTraceHistory(history)
        
        // Step 3: Upsert the computed state back to the analytics database tables
        if err := tr.repo.SaveDiffVerdict(ctx, report.TraceID, verdict); err != nil {
            log.Printf("[Engine] State execution save fault for trace %s: %v", report.TraceID, err)
        }
        cancel()
    }
}
```
 
5. Phased Implementation Roadmap
To maintain code stability and prevent context limits from breaking compilation, Cursor must execute this refactor in five distinct steps:

Step 1 [CURRENT FOCUS]: Interface and Core Domain Models definitions (internal/storage/).

Step 2: Pure-Go SQLite adapter implementation backed by concurrent performance configuration statements (PRAGMA journal_mode=WAL;).

Step 3: Concurrency-gate Multiplexer Engine implementation (TraceRouter worker pools).

Step 4: Rewrite the diff analysis logic from static snapshots to historical slice evaluations ([]storage.RawReport). Group arrays into map buckets by signature before pairing indexes to isolate regressions and prevent timeline alignment drift.

Step 5: Handler migration (Envoy ext_proc, OTLP gRPC/HTTP, and RabbitMQ) to directly target router.Route(report) and clean out legacy volatile map storage.