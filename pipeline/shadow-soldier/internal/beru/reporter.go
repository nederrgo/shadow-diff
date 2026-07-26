package beru

import (
	"context"
	"errors"
	"hash/fnv"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/shadow-diff/shadow-soldier/internal/parsers"
)

// Defaults for the in-memory work queue. The queue exists so the proxy's copy
// loop can hand off a report in constant time and get straight back to moving
// the application's bytes.
const (
	DefaultQueueSize  = 10000
	DefaultWorkers    = 5
	DefaultMaxRetries = 3
	baseBackoff       = 50 * time.Millisecond
)

// Reporter buffers decoded queries and posts them to Beru.
//
// Enqueue never blocks and never fails. When the queue is full the report is
// dropped and counted — the application's database path must not be slowed by a
// slow or unreachable analysis sink, which is the same "do no harm" rule the
// platform applies to production capture.
type Reporter struct {
	Log            *slog.Logger
	Client         *Client
	Role           string
	ShadowTestName string
	ShadowPod      string
	Workers        int
	QueueSize      int
	MaxRetries     int

	// shards is one queue per worker. Reports are routed by trace id so every
	// report for a trace is posted by a single goroutine, in order.
	//
	// This matters because Beru pairs egress reports by their index within a
	// signature bucket, ordered by the arrival time it stamps itself. A shared
	// queue drained by N workers would reorder same-signature reports within a
	// trace and diff query 1 against query 2 — a payload mismatch that is purely
	// an artefact of concurrency. It mirrors the FNV sharding Beru's own
	// TraceRouter uses for exactly the same reason.
	shards []chan parsers.QueryReport

	dropped  atomic.Int64
	untraced atomic.Int64
	failed   atomic.Int64
	sent     atomic.Int64

	wg   sync.WaitGroup
	once sync.Once
}

// Start launches the worker pool. It returns immediately.
func (r *Reporter) Start() {
	if r.Log == nil {
		r.Log = slog.Default()
	}
	if r.Workers <= 0 {
		r.Workers = DefaultWorkers
	}
	if r.QueueSize <= 0 {
		r.QueueSize = DefaultQueueSize
	}
	if r.MaxRetries <= 0 {
		r.MaxRetries = DefaultMaxRetries
	}

	perShard := max(r.QueueSize/r.Workers, 1)
	r.shards = make([]chan parsers.QueryReport, r.Workers)
	for i := range r.shards {
		r.shards[i] = make(chan parsers.QueryReport, perShard)
		r.wg.Add(1)
		go r.worker(r.shards[i])
	}
}

// Enqueue queues one report. Safe for concurrent use; never blocks.
func (r *Reporter) Enqueue(q parsers.QueryReport) {
	// A report with no trace context cannot be correlated with the other two
	// roles, so it can never take part in a diff. Dropping it here is the same
	// policy Kaisel applies to untraced HTTP captures.
	if q.TraceID == "" {
		r.untraced.Add(1)
		return
	}
	if len(r.shards) == 0 {
		r.dropped.Add(1)
		return
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(q.TraceID))
	shard := r.shards[h.Sum32()%uint32(len(r.shards))]
	select {
	case shard <- q:
	default:
		r.dropped.Add(1)
	}
}

// Stop closes the queues and waits for in-flight reports to drain, so a pod
// shutting down still delivers what it has already parsed.
func (r *Reporter) Stop() {
	r.once.Do(func() {
		for _, s := range r.shards {
			close(s)
		}
	})
	r.wg.Wait()
	r.Log.Info("shadow-soldier reporter stopped",
		"sent", r.sent.Load(),
		"dropped_queue_full", r.dropped.Load(),
		"dropped_untraced", r.untraced.Load(),
		"failed", r.failed.Load())
}

// Stats returns the reporter's counters. Drops are always counted, never silent:
// a run that reported nothing must be distinguishable from a run that had
// nothing to report.
func (r *Reporter) Stats() (sent, droppedQueueFull, droppedUntraced, failed int64) {
	return r.sent.Load(), r.dropped.Load(), r.untraced.Load(), r.failed.Load()
}

func (r *Reporter) worker(in <-chan parsers.QueryReport) {
	defer r.wg.Done()
	for q := range in {
		if err := r.post(q); err != nil {
			r.failed.Add(1)
			r.Log.Warn("report dropped after retries",
				"protocol", q.Protocol, "signature", q.Signature(), "err", err)
			continue
		}
		r.sent.Add(1)
	}
}

func (r *Reporter) post(q parsers.QueryReport) error {
	payload, err := BuildPayload(q, r.ShadowPod)
	if err != nil {
		return err
	}
	report := Report{
		TraceID:        q.TraceID,
		Workload:       r.Role,
		Protocol:       q.Protocol,
		Signature:      q.Signature(),
		Payload:        payload,
		ShadowTestName: r.ShadowTestName,
	}

	var lastErr error
	for attempt := 0; attempt <= r.MaxRetries; attempt++ {
		if attempt > 0 {
			time.Sleep(baseBackoff << (attempt - 1))
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		err := r.Client.PostReport(ctx, report)
		cancel()
		if err == nil {
			return nil
		}
		lastErr = err
		if !retryable(err) {
			return err
		}
	}
	return lastErr
}

// retryable reports whether err is worth another attempt.
//
// Beru does not deduplicate: AppendReport is a plain INSERT with no idempotency
// key, so a report delivered twice becomes two rows and reads as an extra egress
// call — a regression that never happened. Retries are therefore limited to
// failures where the request provably did not reach the handler.
//
// A timeout is deliberately *not* retried. It is the one failure that cannot be
// told apart from "Beru accepted it and was slow to answer", and a false extra
// egress is a worse outcome than a missing one.
func retryable(err error) bool {
	var statusErr *statusError
	if errors.As(err, &statusErr) {
		// 4xx is a malformed report; sending it again changes nothing.
		return statusErr.Code >= 500
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	// Connection refused, DNS failure, reset: the request never landed.
	return true
}
