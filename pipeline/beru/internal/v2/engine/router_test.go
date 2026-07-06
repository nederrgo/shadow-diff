package engine

import (
	"context"
	"database/sql"
	"fmt"
	"hash/fnv"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shadow-diff/beru/internal/v2/storage"
)

type recordingRepo struct {
	mu sync.Mutex

	appendOrder []string
	inflight    int
	maxInflight int

	appendRemaining atomic.Int32
	appendDone      chan struct{}
	appendDoneOnce  sync.Once

	blockRelease chan struct{}
	entered      chan struct{}
}

func newRecordingRepo() *recordingRepo {
	return &recordingRepo{
		appendDone: make(chan struct{}),
	}
}

func (r *recordingRepo) expectAppends(n int) {
	r.appendRemaining.Store(int32(n))
}

func (r *recordingRepo) waitAppends(t *testing.T) {
	t.Helper()
	select {
	case <-r.appendDone:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for append reports")
	}
}

func (r *recordingRepo) noteAppend(signature string) {
	r.mu.Lock()
	r.appendOrder = append(r.appendOrder, signature)
	r.mu.Unlock()

	if r.appendRemaining.Add(-1) == 0 {
		r.appendDoneOnce.Do(func() { close(r.appendDone) })
	}
}

func (r *recordingRepo) enterAppend() {
	r.mu.Lock()
	r.inflight++
	if r.inflight > r.maxInflight {
		r.maxInflight = r.inflight
	}
	r.mu.Unlock()
}

func (r *recordingRepo) leaveAppend() {
	r.mu.Lock()
	r.inflight--
	r.mu.Unlock()
}

func (r *recordingRepo) AppendReport(ctx context.Context, report *storage.RawReport) ([]storage.RawReport, error) {
	r.enterAppend()
	defer r.leaveAppend()

	if r.blockRelease != nil {
		select {
		case r.entered <- struct{}{}:
		default:
		}
		<-r.blockRelease
	}

	r.noteAppend(report.Signature)

	return []storage.RawReport{*report}, nil
}

func (r *recordingRepo) SaveDiffVerdict(ctx context.Context, traceID string, verdict *storage.VerdictState) error {
	return nil
}

func (r *recordingRepo) ListReports(ctx context.Context, traceID, protocol string) ([]storage.RawReport, error) {
	return nil, nil
}

func (r *recordingRepo) ListTraceGroups(ctx context.Context, shadowTestName string, limit int) ([]storage.TraceGroup, error) {
	return nil, nil
}

func (r *recordingRepo) GetVerdict(ctx context.Context, traceID string) (*storage.VerdictState, error) {
	return nil, nil
}

func workerIndex(traceID string, workerCount int) uint32 {
	hasher := fnv.New32a()
	hasher.Write([]byte(traceID))
	return hasher.Sum32() % uint32(workerCount)
}

func traceIDForWorker(target uint32, workerCount int) string {
	for i := 0; i < 100_000; i++ {
		id := fmt.Sprintf("trace-%d", i)
		if workerIndex(id, workerCount) == target {
			return id
		}
	}
	panic(fmt.Sprintf("no trace ID maps to worker %d", target))
}

func TestRoute_sameTraceID_processedSequentially(t *testing.T) {
	repo := newRecordingRepo()
	repo.expectAppends(3)
	router := NewTraceRouter(2, repo, nil)

	traceID := "trace-seq"
	for i := 0; i < 3; i++ {
		router.Route(&storage.RawReport{
			TraceID:    traceID,
			Signature:  fmt.Sprintf("sig-%d", i),
			Protocol:   "http",
			Direction:  storage.DirectionIngress,
			CapturedAt: time.Now().UTC(),
		})
	}

	repo.waitAppends(t)

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

func TestRoute_differentTraceIDs_processConcurrently(t *testing.T) {
	const workerCount = 4
	repo := newRecordingRepo()
	repo.blockRelease = make(chan struct{})
	repo.entered = make(chan struct{}, workerCount)

	traceA := traceIDForWorker(0, workerCount)
	traceB := traceIDForWorker(1, workerCount)
	if traceA == traceB {
		t.Fatal("expected distinct trace IDs for different workers")
	}

	router := NewTraceRouter(workerCount, repo, nil)

	done := make(chan struct{}, 2)
	go func() {
		router.Route(&storage.RawReport{
			TraceID:    traceA,
			Signature:  "sig-a",
			Protocol:   "mongodb",
			Direction:  storage.DirectionEgress,
			CapturedAt: time.Now().UTC(),
		})
		done <- struct{}{}
	}()
	go func() {
		router.Route(&storage.RawReport{
			TraceID:    traceB,
			Signature:  "sig-b",
			Protocol:   "mongodb",
			Direction:  storage.DirectionEgress,
			CapturedAt: time.Now().UTC(),
		})
		done <- struct{}{}
	}()

	entered := 0
	deadline := time.After(5 * time.Second)
	for entered < 2 {
		select {
		case <-repo.entered:
			entered++
		case <-deadline:
			t.Fatalf("only %d workers entered AppendReport concurrently", entered)
		}
	}

	repo.mu.Lock()
	maxInflight := repo.maxInflight
	repo.mu.Unlock()
	if maxInflight < 2 {
		t.Fatalf("maxInflight = %d, want >= 2", maxInflight)
	}

	close(repo.blockRelease)
	<-done
	<-done
}

func TestRoute_withSQLiteRepository(t *testing.T) {
	path := filepath.Join(t.TempDir(), "engine.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	t.Cleanup(func() { _ = db.Close() })

	repo, err := storage.NewSQLiteRepository(db)
	if err != nil {
		t.Fatal(err)
	}

	repoRecorder := &sqliteSmokeRepo{
		TraceRepository: repo,
		appendDone:      make(chan struct{}),
	}
	repoRecorder.appendRemaining.Store(2)

	router := NewTraceRouter(1, repoRecorder, nil)
	capturedAt := time.Now().UTC()
	payload := []byte(`{}`)
	router.Route(&storage.RawReport{
		TraceID:      "trace-smoke",
		ShadowRole:   "control-a",
		Protocol:     "http",
		Direction:    storage.DirectionIngress,
		Signature:    "http:GET:/health",
		PayloadBytes: payload,
		CapturedAt:   capturedAt,
	})
	router.Route(&storage.RawReport{
		TraceID:      "trace-smoke",
		ShadowRole:   "candidate",
		Protocol:     "http",
		Direction:    storage.DirectionIngress,
		Signature:    "http:GET:/health",
		PayloadBytes: payload,
		CapturedAt:   capturedAt,
	})

	select {
	case <-repoRecorder.appendDone:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for router to append report")
	}

	ctx := context.Background()
	var status string
	err = db.QueryRowContext(ctx, `SELECT status FROM verdicts WHERE trace_id = ?`, "trace-smoke").Scan(&status)
	if err != nil {
		t.Fatal(err)
	}
	if status != "MATCH" {
		t.Fatalf("verdict status = %q, want MATCH", status)
	}
}

type sqliteSmokeRepo struct {
	storage.TraceRepository
	appendRemaining atomic.Int32
	appendDone      chan struct{}
	appendDoneOnce  sync.Once
}

func (r *sqliteSmokeRepo) AppendReport(ctx context.Context, report *storage.RawReport) ([]storage.RawReport, error) {
	out, err := r.TraceRepository.AppendReport(ctx, report)
	if err != nil {
		return out, err
	}
	if r.appendRemaining.Add(-1) == 0 {
		r.appendDoneOnce.Do(func() { close(r.appendDone) })
	}
	return out, nil
}
