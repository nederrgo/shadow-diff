package storage

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"

	"github.com/shadow-diff/beru/internal/model"
)

func openTestWAL(t *testing.T) *WALStore {
	t.Helper()
	dir := t.TempDir()
	walPath := filepath.Join(dir, "beru_wal.db")
	dlqPath := filepath.Join(dir, "dead_letters.jsonl")
	bdb, err := bolt.Open(walPath, 0o600, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := bdb.Update(func(tx *bolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists([]byte(walBucketName))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	w := &WALStore{
		log:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		db:         bdb,
		walPath:    walPath,
		dlqPath:    dlqPath,
		timeout:    time.Second,
		notify:     make(chan struct{}, 1),
		stop:       make(chan struct{}),
		stopped:    make(chan struct{}),
		workers:    make([]chan TraceBatch, walWorkerCount),
		completion: make(chan completionEvent, 8),
	}
	for i := range w.workers {
		w.workers[i] = make(chan TraceBatch, walChannelBuf)
	}
	t.Cleanup(func() { _ = bdb.Close() })
	return w
}

func TestWAL_claimSkipsInFlight(t *testing.T) {
	w := openTestWAL(t)
	rep := model.RawReport{
		TraceID: "trace-100", ShadowRole: "control-a", Protocol: "http",
		Direction: model.DirectionIngress, Signature: "http:GET:/x",
		PayloadBytes: []byte(`{}`), CapturedAt: time.Now().UTC(),
	}
	if err := w.appendWAL(rep); err != nil {
		t.Fatal(err)
	}

	inFlight := map[string]bool{}
	nextRetry := map[string]time.Time{}
	w.claimAndDispatch(inFlight, nextRetry)
	if !inFlight["trace-100"] {
		t.Fatal("expected trace-100 claimed after first dispatch")
	}
	idx := int(fnv32("trace-100") % uint32(len(w.workers)))
	select {
	case <-w.workers[idx]:
	default:
		t.Fatal("expected a batch on the sticky worker channel")
	}

	if err := w.appendWAL(rep); err != nil {
		t.Fatal(err)
	}
	w.claimAndDispatch(inFlight, nextRetry)
	select {
	case batch := <-w.workers[idx]:
		t.Fatalf("unexpected second enqueue: %+v", batch.TraceID)
	default:
	}
}

func TestWAL_deadLetterAfterThreeRetries(t *testing.T) {
	w := openTestWAL(t)
	rep := model.RawReport{
		TraceID: "trace-bad", ShadowRole: "control-a", Protocol: "http",
		Direction: model.DirectionIngress, Signature: "http:GET:/bad",
		PayloadBytes: []byte(`{}`), CapturedAt: time.Now().UTC(),
	}
	if err := w.appendWAL(rep); err != nil {
		t.Fatal(err)
	}

	batch := loadSingleBatch(t, w)
	flushErr := errors.New("forced flush failure")
	for i := 1; i <= maxFlushRetries; i++ {
		dlq, err := w.bumpRetriesOrDeadLetter(batch, flushErr)
		if err != nil {
			t.Fatal(err)
		}
		if i < maxFlushRetries {
			if dlq {
				t.Fatalf("dead-lettered too early on attempt %d", i)
			}
			batch = loadSingleBatch(t, w)
			continue
		}
		if !dlq {
			t.Fatal("expected dead-letter on third failure")
		}
	}

	var remaining int
	_ = w.db.View(func(tx *bolt.Tx) error {
		remaining = tx.Bucket([]byte(walBucketName)).Stats().KeyN
		return nil
	})
	if remaining != 0 {
		t.Fatalf("WAL keys remaining = %d, want 0", remaining)
	}
	body, err := os.ReadFile(w.dlqPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "trace-bad") || !strings.Contains(string(body), "forced flush failure") {
		t.Fatalf("dead letter contents = %q", body)
	}
}

func loadSingleBatch(t *testing.T, w *WALStore) TraceBatch {
	t.Helper()
	var batch TraceBatch
	err := w.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(walBucketName))
		return b.ForEach(func(k, v []byte) error {
			var e walEntry
			if err := json.Unmarshal(v, &e); err != nil {
				return err
			}
			batch.TraceID = e.Report.TraceID
			batch.Keys = append(batch.Keys, append([]byte(nil), k...))
			batch.Entries = append(batch.Entries, e)
			return nil
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(batch.Keys) == 0 {
		t.Fatal("expected WAL keys")
	}
	return batch
}
