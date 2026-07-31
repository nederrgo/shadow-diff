package storage

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	bolt "go.etcd.io/bbolt"

	v2storage "github.com/shadow-diff/beru/internal/v2/storage"
)

const (
	walBucketName      = "pending"
	defaultWALPath     = "/data/beru_wal.db"
	defaultDeadLetter  = "/data/dead_letters.jsonl"
	walWorkerCount     = 8
	walChannelBuf      = 64
	walBatchMaxOps     = 100
	walScanInterval    = 50 * time.Millisecond
	walMaxFileBytes    = 1 << 30 // 1 GiB
	walDropHeadPercent = 10
	maxFlushRetries    = 3
)

var backoffSteps = []time.Duration{100 * time.Millisecond, 500 * time.Millisecond, time.Second, 2 * time.Second, 5 * time.Second}

var (
	_ RunStore                    = (*WALStore)(nil)
	_ v2storage.TraceRepository   = (*WALStore)(nil)
)

type walEntry struct {
	RetryCount int                 `json:"retry_count"`
	Report     v2storage.RawReport `json:"report"`
}

type TraceBatch struct {
	TraceID string
	Keys    [][]byte
	Entries []walEntry
}

type completionEvent struct {
	TraceID    string
	OK         bool
	DeadLetter bool
	Err        error
}

// WALStore wraps PostgresStore: ingest appends to Bbolt; a claimed flusher pool
// drains per-trace batches under pg_advisory_xact_lock.
type WALStore struct {
	pg         *PostgresStore
	log        *slog.Logger
	db         *bolt.DB
	walPath    string
	dlqPath    string
	timeout    time.Duration
	seq        atomic.Uint64
	notify     chan struct{}
	stop       chan struct{}
	stopped    chan struct{}
	workers    []chan TraceBatch
	completion chan completionEvent
}

// WrapWithWAL opens the disk WAL and starts the dispatcher + flusher workers.
func WrapWithWAL(log *slog.Logger, pg *PostgresStore) (*WALStore, error) {
	if log == nil {
		log = slog.Default()
	}
	walPath := os.Getenv("BERU_WAL_PATH")
	if walPath == "" {
		walPath = defaultWALPath
	}
	dlqPath := os.Getenv("BERU_DEAD_LETTER_PATH")
	if dlqPath == "" {
		dlqPath = defaultDeadLetter
	}
	if err := os.MkdirAll(filepath.Dir(walPath), 0o755); err != nil {
		return nil, fmt.Errorf("wal parent dir: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(dlqPath), 0o755); err != nil {
		return nil, fmt.Errorf("dlq parent dir: %w", err)
	}
	bdb, err := bolt.Open(walPath, 0o600, &bolt.Options{Timeout: time.Second})
	if err != nil {
		return nil, fmt.Errorf("open wal: %w", err)
	}
	if err := bdb.Update(func(tx *bolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists([]byte(walBucketName))
		return err
	}); err != nil {
		bdb.Close()
		return nil, err
	}

	w := &WALStore{
		pg:         pg,
		log:        log,
		db:         bdb,
		walPath:    walPath,
		dlqPath:    dlqPath,
		timeout:    traceTimeoutFromEnv(),
		notify:     make(chan struct{}, 1),
		stop:       make(chan struct{}),
		stopped:    make(chan struct{}),
		workers:    make([]chan TraceBatch, walWorkerCount),
		completion: make(chan completionEvent, walWorkerCount*walChannelBuf),
	}
	var maxSeq uint64
	_ = bdb.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(walBucketName))
		if b == nil {
			return nil
		}
		c := b.Cursor()
		if k, _ := c.Last(); len(k) == 8 {
			maxSeq = binary.BigEndian.Uint64(k)
		}
		return nil
	})
	w.seq.Store(maxSeq)

	for i := 0; i < walWorkerCount; i++ {
		w.workers[i] = make(chan TraceBatch, walChannelBuf)
		go w.workerLoop(w.workers[i])
	}
	go w.dispatchLoop()
	log.Info("WAL flusher ready", "path", walPath, "workers", walWorkerCount, "dlq", dlqPath)
	return w, nil
}

func traceTimeoutFromEnv() time.Duration {
	v := strings.TrimSpace(os.Getenv("BERU_TRACE_TIMEOUT"))
	if v == "" {
		return 10 * time.Second
	}
	if secs, err := strconv.Atoi(v); err == nil && secs > 0 {
		return time.Duration(secs) * time.Second
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		return 10 * time.Second
	}
	return d
}

// walFlushTimeoutFromEnv bounds one Postgres flush attempt (default 30s).
// Lower it in integration fixtures so poison-pill DLQ is reachable quickly when PG is unreachable.
func walFlushTimeoutFromEnv() time.Duration {
	v := strings.TrimSpace(os.Getenv("BERU_WAL_FLUSH_TIMEOUT"))
	if v == "" {
		return 30 * time.Second
	}
	if secs, err := strconv.Atoi(v); err == nil && secs > 0 {
		return time.Duration(secs) * time.Second
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		return 30 * time.Second
	}
	return d
}

// Close stops flushers and closes Bbolt + Postgres.
func (w *WALStore) Close() error {
	close(w.stop)
	<-w.stopped
	errWAL := w.db.Close()
	errPG := w.pg.Close()
	if errWAL != nil {
		return errWAL
	}
	return errPG
}

func (w *WALStore) DefaultShadowTestName() string { return w.pg.DefaultShadowTestName() }
func (w *WALStore) EnsureShadowTest(ctx context.Context, name string) error {
	return w.pg.EnsureShadowTest(ctx, name)
}
func (w *WALStore) NoisePathsForTest(ctx context.Context, name string) (map[string]struct{}, error) {
	return w.pg.NoisePathsForTest(ctx, name)
}
func (w *WALStore) AddNoiseFilter(ctx context.Context, name, path string) error {
	return w.pg.AddNoiseFilter(ctx, name, path)
}
func (w *WALStore) ListNoiseFilters(ctx context.Context, name string) ([]string, error) {
	return w.pg.ListNoiseFilters(ctx, name)
}
func (w *WALStore) ListShadowTests(ctx context.Context, limit int) ([]ShadowTest, error) {
	return w.pg.ListShadowTests(ctx, limit)
}
func (w *WALStore) GetShadowTest(ctx context.Context, id int64) (ShadowTest, error) {
	return w.pg.GetShadowTest(ctx, id)
}
func (w *WALStore) ListReports(ctx context.Context, traceID, protocol string) ([]v2storage.RawReport, error) {
	return w.pg.ListReports(ctx, traceID, protocol)
}
func (w *WALStore) ListTraceGroups(ctx context.Context, shadowTestName string, limit int) ([]v2storage.TraceGroup, error) {
	return w.pg.ListTraceGroups(ctx, shadowTestName, limit)
}
func (w *WALStore) GetVerdict(ctx context.Context, traceID string) (*v2storage.VerdictState, error) {
	return w.pg.GetVerdict(ctx, traceID)
}
func (w *WALStore) ListStaleIncompleteTraces(ctx context.Context, olderThan time.Time) ([]v2storage.StaleIncompleteTrace, error) {
	return w.pg.ListStaleIncompleteTraces(ctx, olderThan)
}

// AppendReport appends to the disk WAL and returns immediately.
func (w *WALStore) AppendReport(_ context.Context, report *v2storage.RawReport) ([]v2storage.RawReport, error) {
	if report == nil {
		return nil, fmt.Errorf("append report: nil report")
	}
	if err := w.appendWAL(*report); err != nil {
		return nil, err
	}
	w.kick()
	return []v2storage.RawReport{*report}, nil
}

// SaveDiffVerdict runs under the per-trace advisory lock (reaper / sync callers).
func (w *WALStore) SaveDiffVerdict(ctx context.Context, traceID string, verdict *v2storage.VerdictState) error {
	return w.pg.saveDiffVerdictUnderLock(ctx, traceID, verdict)
}

func (w *WALStore) kick() {
	select {
	case w.notify <- struct{}{}:
	default:
	}
}

func (w *WALStore) appendWAL(report v2storage.RawReport) error {
	if err := w.maybeDropHead(); err != nil {
		w.log.Warn("WAL overflow drop-head failed", "err", err)
	}
	entry := walEntry{RetryCount: 0, Report: report}
	payload, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	seq := w.seq.Add(1)
	key := make([]byte, 8)
	binary.BigEndian.PutUint64(key, seq)
	return w.db.Batch(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(walBucketName))
		return b.Put(key, payload)
	})
}

func (w *WALStore) maybeDropHead() error {
	fi, err := os.Stat(w.walPath)
	if err != nil || fi.Size() <= walMaxFileBytes {
		return nil
	}
	return w.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(walBucketName))
		if b == nil {
			return nil
		}
		n := b.Stats().KeyN
		if n == 0 {
			return nil
		}
		drop := n * walDropHeadPercent / 100
		if drop < 1 {
			drop = 1
		}
		c := b.Cursor()
		deleted := 0
		for k, _ := c.First(); k != nil && deleted < drop; k, _ = c.Next() {
			if err := b.Delete(k); err != nil {
				return err
			}
			deleted++
		}
		w.log.Warn("WAL overflow: dropped oldest pending entries", "dropped", deleted, "size_bytes", fi.Size())
		return nil
	})
}

func (w *WALStore) dispatchLoop() {
	defer close(w.stopped)
	inFlight := make(map[string]bool)
	nextRetryAt := make(map[string]time.Time)
	backoffAttempt := make(map[string]int)
	ticker := time.NewTicker(walScanInterval)
	defer ticker.Stop()

	for {
		select {
		case <-w.stop:
			return
		case ev := <-w.completion:
			w.handleCompletion(ev, inFlight, nextRetryAt, backoffAttempt)
			// Drain the rest without blocking.
			for {
				select {
				case ev := <-w.completion:
					w.handleCompletion(ev, inFlight, nextRetryAt, backoffAttempt)
				default:
					goto scan
				}
			}
		case <-w.notify:
		case <-ticker.C:
		}
	scan:
		// Also drain any completions that arrived during scan prep.
		for {
			select {
			case ev := <-w.completion:
				w.handleCompletion(ev, inFlight, nextRetryAt, backoffAttempt)
			default:
				goto dispatch
			}
		}
	dispatch:
		w.claimAndDispatch(inFlight, nextRetryAt)
	}
}

func (w *WALStore) handleCompletion(
	ev completionEvent,
	inFlight map[string]bool,
	nextRetryAt map[string]time.Time,
	backoffAttempt map[string]int,
) {
	delete(inFlight, ev.TraceID)
	if ev.OK || ev.DeadLetter {
		delete(nextRetryAt, ev.TraceID)
		delete(backoffAttempt, ev.TraceID)
		return
	}
	attempt := backoffAttempt[ev.TraceID]
	delay := backoffSteps[len(backoffSteps)-1]
	if attempt < len(backoffSteps) {
		delay = backoffSteps[attempt]
	}
	nextRetryAt[ev.TraceID] = time.Now().Add(delay)
	backoffAttempt[ev.TraceID] = attempt + 1
	if ev.Err != nil {
		w.log.Warn("WAL flush failed; will retry", "trace_id", ev.TraceID, "err", ev.Err, "backoff", delay)
	}
}

func (w *WALStore) claimAndDispatch(inFlight map[string]bool, nextRetryAt map[string]time.Time) {
	groups := make(map[string]*TraceBatch)
	var order []string
	_ = w.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(walBucketName))
		if b == nil {
			return nil
		}
		return b.ForEach(func(k, v []byte) error {
			var e walEntry
			if err := json.Unmarshal(v, &e); err != nil {
				return nil // skip corrupt; poison pill will eventually DLQ via retries on other ops
			}
			tid := e.Report.TraceID
			batch, ok := groups[tid]
			if !ok {
				batch = &TraceBatch{TraceID: tid}
				groups[tid] = batch
				order = append(order, tid)
			}
			if len(batch.Keys) >= walBatchMaxOps {
				return nil
			}
			keyCopy := append([]byte(nil), k...)
			batch.Keys = append(batch.Keys, keyCopy)
			batch.Entries = append(batch.Entries, e)
			return nil
		})
	})

	now := time.Now()
	for _, tid := range order {
		if inFlight[tid] {
			continue
		}
		if t, ok := nextRetryAt[tid]; ok && now.Before(t) {
			continue
		}
		batch := *groups[tid]
		idx := int(fnv32(tid) % uint32(len(w.workers)))
		select {
		case w.workers[idx] <- batch:
			inFlight[tid] = true
		default:
			// Channel full — do not claim; retry next scan.
		}
	}
}

func fnv32(s string) uint32 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(s))
	return h.Sum32()
}

func (w *WALStore) workerLoop(ch <-chan TraceBatch) {
	for {
		select {
		case <-w.stop:
			return
		case batch, ok := <-ch:
			if !ok {
				return
			}
			w.processBatch(batch)
		}
	}
}

func (w *WALStore) processBatch(batch TraceBatch) {
	ev := completionEvent{TraceID: batch.TraceID}
	defer func() { w.completion <- ev }()

	reports := make([]v2storage.RawReport, len(batch.Entries))
	for i := range batch.Entries {
		reports[i] = batch.Entries[i].Report
	}

	ctx, cancel := context.WithTimeout(context.Background(), walFlushTimeoutFromEnv())
	defer cancel()

	var noise map[string]struct{}
	if len(reports) > 0 && reports[0].ShadowTestName != "" {
		noise, _ = w.pg.NoisePathsForTest(ctx, reports[0].ShadowTestName)
	}

	if err := w.pg.flushReportsAndEvaluate(ctx, reports, noise, w.timeout); err != nil {
		ev.Err = err
		dlq, bumpErr := w.bumpRetriesOrDeadLetter(batch, err)
		if bumpErr != nil {
			w.log.Error("WAL retry/DLQ update failed", "trace_id", batch.TraceID, "err", bumpErr)
		}
		ev.DeadLetter = dlq
		return
	}
	if err := w.deleteKeys(batch.Keys); err != nil {
		ev.Err = err
		w.log.Error("WAL delete after commit failed", "trace_id", batch.TraceID, "err", err)
		return
	}
	ev.OK = true
}

func (w *WALStore) deleteKeys(keys [][]byte) error {
	return w.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(walBucketName))
		for _, k := range keys {
			if err := b.Delete(k); err != nil {
				return err
			}
		}
		return nil
	})
}

func (w *WALStore) bumpRetriesOrDeadLetter(batch TraceBatch, flushErr error) (deadLetter bool, err error) {
	var maxRetry int
	err = w.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(walBucketName))
		for i, k := range batch.Keys {
			raw := b.Get(k)
			if raw == nil {
				continue
			}
			var e walEntry
			if err := json.Unmarshal(raw, &e); err != nil {
				e = batch.Entries[i]
			}
			e.RetryCount++
			if e.RetryCount > maxRetry {
				maxRetry = e.RetryCount
			}
			payload, err := json.Marshal(e)
			if err != nil {
				return err
			}
			if err := b.Put(k, payload); err != nil {
				return err
			}
			batch.Entries[i] = e
		}
		return nil
	})
	if err != nil {
		return false, err
	}
	if maxRetry < maxFlushRetries {
		return false, nil
	}
	if err := w.writeDeadLetter(batch, flushErr); err != nil {
		return false, err
	}
	if err := w.deleteKeys(batch.Keys); err != nil {
		return false, err
	}
	w.log.Error("Discarding failed WAL batch after max retries",
		"trace_id", batch.TraceID, "retries", maxRetry, "err", flushErr)
	return true, nil
}

func (w *WALStore) writeDeadLetter(batch TraceBatch, flushErr error) error {
	f, err := os.OpenFile(w.dlqPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	rec := map[string]any{
		"trace_id":   batch.TraceID,
		"error":      fmt.Sprint(flushErr),
		"ts":         time.Now().UTC().Format(time.RFC3339Nano),
		"entries":    batch.Entries,
		"key_count":  len(batch.Keys),
	}
	line, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	_, err = f.Write(append(line, '\n'))
	return err
}
