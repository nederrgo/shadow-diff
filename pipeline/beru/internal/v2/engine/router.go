package engine

import (
	"context"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/shadow-diff/beru/internal/storage"
	"github.com/shadow-diff/beru/internal/v2/diff"
	v2storage "github.com/shadow-diff/beru/internal/v2/storage"
)

const (
	defaultTraceTimeout = 10 * time.Second
	reaperInterval      = 2 * time.Second
)

type TraceRouter struct {
	repo    v2storage.TraceRepository
	runs    storage.RunStore
	timeout time.Duration
	stop    chan struct{}
}

func NewTraceRouter(repo v2storage.TraceRepository, runs storage.RunStore) *TraceRouter {
	return NewTraceRouterWithTimeout(repo, runs, TraceTimeoutFromEnv())
}

func NewTraceRouterWithTimeout(repo v2storage.TraceRepository, runs storage.RunStore, timeout time.Duration) *TraceRouter {
	if timeout <= 0 {
		timeout = defaultTraceTimeout
	}
	tr := &TraceRouter{
		repo:    repo,
		runs:    runs,
		timeout: timeout,
		stop:    make(chan struct{}),
	}
	go tr.startReaper()
	return tr
}

// TraceTimeoutFromEnv reads BERU_TRACE_TIMEOUT (seconds or Go duration). Default 10s.
func TraceTimeoutFromEnv() time.Duration {
	v := strings.TrimSpace(os.Getenv("BERU_TRACE_TIMEOUT"))
	if v == "" {
		return defaultTraceTimeout
	}
	if secs, err := strconv.Atoi(v); err == nil && secs > 0 {
		return time.Duration(secs) * time.Second
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		return defaultTraceTimeout
	}
	return d
}

// Route appends to the local WAL on the caller's goroutine. A nil error means
// the report is durable on Bbolt; Postgres flush stays async via WAL kick.
func (tr *TraceRouter) Route(report *v2storage.RawReport) error {
	if report == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if tr.runs != nil && report.ShadowTestName != "" {
		_ = tr.runs.EnsureShadowTest(ctx, report.ShadowTestName)
	}

	// Verdict evaluation runs in the WAL flusher under pg_advisory_xact_lock.
	_, err := tr.repo.AppendReport(ctx, report)
	return err
}

// startReaper periodically finalizes incomplete traces past the timeout as WAITING_FOR_ROLES.
// Each list/load/save uses a short context — no long-lived transaction across the sweep.
func (tr *TraceRouter) startReaper() {
	ticker := time.NewTicker(reaperInterval)
	defer ticker.Stop()
	for {
		select {
		case <-tr.stop:
			return
		case <-ticker.C:
			tr.reapOnce()
		}
	}
}

func (tr *TraceRouter) reapOnce() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	olderThan := time.Now().UTC().Add(-tr.timeout)
	stale, err := tr.repo.ListStaleIncompleteTraces(ctx, olderThan)
	cancel()
	if err != nil {
		log.Printf("[Engine] Reaper list fault: %v", err)
		return
	}
	for _, candidate := range stale {
		tr.reapTrace(candidate.TraceID)
	}
}

func (tr *TraceRouter) reapTrace(traceID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	history, err := tr.repo.ListReports(ctx, traceID, "")
	if err != nil {
		log.Printf("[Engine] Reaper load fault for trace %s: %v", traceID, err)
		return
	}
	var userNoise map[string]struct{}
	if tr.runs != nil && len(history) > 0 && history[0].ShadowTestName != "" {
		userNoise, _ = tr.runs.NoisePathsForTest(ctx, history[0].ShadowTestName)
	}
	verdict := diff.EvaluateTraceHistory(history, userNoise, diff.EvalOptions{Timeout: tr.timeout})
	if verdict == nil {
		return
	}
	if err := tr.repo.SaveDiffVerdict(ctx, traceID, verdict); err != nil {
		log.Printf("[Engine] Reaper save fault for trace %s: %v", traceID, err)
	}
}
