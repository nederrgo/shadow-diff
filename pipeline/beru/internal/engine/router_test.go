package engine

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/shadow-diff/beru/internal/model"
)

type recordingRepo struct {
	mu sync.Mutex

	appendOrder []string
	appendErr   error
}

func newRecordingRepo() *recordingRepo {
	return &recordingRepo{}
}

func (r *recordingRepo) AppendReport(_ context.Context, report *model.RawReport) ([]model.RawReport, error) {
	if r.appendErr != nil {
		return nil, r.appendErr
	}
	r.mu.Lock()
	r.appendOrder = append(r.appendOrder, report.Signature)
	r.mu.Unlock()
	return []model.RawReport{*report}, nil
}

func (r *recordingRepo) SaveDiffVerdict(ctx context.Context, traceID string, verdict *model.VerdictState) error {
	return nil
}

func (r *recordingRepo) ListReports(ctx context.Context, traceID, protocol string) ([]model.RawReport, error) {
	return nil, nil
}

func (r *recordingRepo) ListTraceGroups(ctx context.Context, shadowTestName string, limit int) ([]model.TraceGroup, error) {
	return nil, nil
}

func (r *recordingRepo) GetVerdict(ctx context.Context, traceID string) (*model.VerdictState, error) {
	return nil, nil
}

func (r *recordingRepo) ListStaleIncompleteTraces(ctx context.Context, olderThan time.Time) ([]model.StaleIncompleteTrace, error) {
	return nil, nil
}

func TestRoute_appendsBeforeReturn(t *testing.T) {
	repo := newRecordingRepo()
	router := NewTraceRouter(repo, nil)

	traceID := "trace-seq"
	for i := 0; i < 3; i++ {
		if err := router.Route(&model.RawReport{
			TraceID:    traceID,
			Signature:  fmt.Sprintf("sig-%d", i),
			Protocol:   "http",
			Direction:  model.DirectionIngress,
			CapturedAt: time.Now().UTC(),
		}); err != nil {
			t.Fatal(err)
		}
	}

	repo.mu.Lock()
	order := append([]string(nil), repo.appendOrder...)
	repo.mu.Unlock()

	want := []string{"sig-0", "sig-1", "sig-2"}
	if len(order) != len(want) {
		t.Fatalf("append order = %v, want %v", order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("append order = %v, want %v", order, want)
		}
	}
}

func TestRoute_surfacesAppendError(t *testing.T) {
	repo := newRecordingRepo()
	repo.appendErr = errors.New("wal full")
	router := NewTraceRouter(repo, nil)

	err := router.Route(&model.RawReport{
		TraceID:    "trace-fail",
		Signature:  "sig",
		Protocol:   "http",
		Direction:  model.DirectionIngress,
		CapturedAt: time.Now().UTC(),
	})
	if err == nil {
		t.Fatal("expected append error")
	}
	if !errors.Is(err, repo.appendErr) && err.Error() != repo.appendErr.Error() {
		t.Fatalf("err = %v, want %v", err, repo.appendErr)
	}
}

func TestRoute_nilReport(t *testing.T) {
	router := NewTraceRouter(newRecordingRepo(), nil)
	if err := router.Route(nil); err != nil {
		t.Fatalf("nil report: %v", err)
	}
}

func TestRoute_ingestOnlyAppends(t *testing.T) {
	repo := newRecordingRepo()
	router := NewTraceRouter(repo, nil)
	capturedAt := time.Now().UTC()
	for _, role := range []string{"control-a", "control-b", "candidate"} {
		if err := router.Route(&model.RawReport{
			TraceID:      "trace-smoke",
			ShadowRole:   role,
			Protocol:     "http",
			Direction:    model.DirectionIngress,
			Signature:    "http:GET:/health",
			StatusCode:   "200",
			PayloadBytes: []byte(`{}`),
			CapturedAt:   capturedAt,
		}); err != nil {
			t.Fatal(err)
		}
	}
	repo.mu.Lock()
	n := len(repo.appendOrder)
	repo.mu.Unlock()
	if n != 3 {
		t.Fatalf("appends = %d, want 3", n)
	}
}

func TestReaper_marksWaitingForRoles(t *testing.T) {
	repo := newMemoryRepo()
	router := NewTraceRouterWithTimeout(repo, nil, 50*time.Millisecond)
	old := time.Now().UTC().Add(-200 * time.Millisecond)
	ctx := context.Background()
	if _, err := repo.AppendReport(ctx, &model.RawReport{
		TraceID: "reaper-trace", ShadowRole: "control-a", Protocol: "mongodb",
		Direction: model.DirectionEgress, Signature: "mongodb:x",
		PayloadBytes: []byte(`{}`), CapturedAt: old,
	}); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		router.reapOnce()
		v, err := repo.GetVerdict(ctx, "reaper-trace")
		if err != nil {
			t.Fatal(err)
		}
		if v != nil && v.Status == model.StatusWaitingForRoles {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("reaper did not mark WAITING_FOR_ROLES")
}

// memoryRepo is enough for reaper tests: stores reports + verdicts in maps.
type memoryRepo struct {
	mu       sync.Mutex
	reports  map[string][]model.RawReport
	verdicts map[string]*model.VerdictState
}

func newMemoryRepo() *memoryRepo {
	return &memoryRepo{
		reports:  map[string][]model.RawReport{},
		verdicts: map[string]*model.VerdictState{},
	}
}

func (m *memoryRepo) AppendReport(_ context.Context, report *model.RawReport) ([]model.RawReport, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reports[report.TraceID] = append(m.reports[report.TraceID], *report)
	out := append([]model.RawReport(nil), m.reports[report.TraceID]...)
	return out, nil
}

func (m *memoryRepo) SaveDiffVerdict(_ context.Context, traceID string, verdict *model.VerdictState) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := *verdict
	m.verdicts[traceID] = &cp
	return nil
}

func (m *memoryRepo) ListReports(_ context.Context, traceID, _ string) ([]model.RawReport, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]model.RawReport(nil), m.reports[traceID]...), nil
}

func (m *memoryRepo) ListTraceGroups(context.Context, string, int) ([]model.TraceGroup, error) {
	return nil, nil
}

func (m *memoryRepo) GetVerdict(_ context.Context, traceID string) (*model.VerdictState, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v := m.verdicts[traceID]
	if v == nil {
		return nil, nil
	}
	cp := *v
	return &cp, nil
}

func (m *memoryRepo) ListStaleIncompleteTraces(_ context.Context, olderThan time.Time) ([]model.StaleIncompleteTrace, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []model.StaleIncompleteTrace
	for tid, reps := range m.reports {
		if len(reps) == 0 {
			continue
		}
		first := reps[0].CapturedAt
		roles := map[string]bool{}
		for _, r := range reps {
			roles[r.ShadowRole] = true
			if r.CapturedAt.Before(first) {
				first = r.CapturedAt
			}
		}
		if first.After(olderThan) {
			continue
		}
		if !roles["control-a"] || !roles["control-b"] || !roles["candidate"] {
			out = append(out, model.StaleIncompleteTrace{TraceID: tid, FirstSeen: first})
		}
	}
	return out, nil
}
