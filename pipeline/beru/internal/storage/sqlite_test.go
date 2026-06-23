package storage

import (
	"context"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/shadow-diff/beru/internal/diff"
)

func testDB(t *testing.T) *DB {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "test.db")
	db, err := OpenAt(slog.Default(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestOpen_migrateAndSeed(t *testing.T) {
	db := testDB(t)
	runs, err := db.ListShadowTests(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) == 0 {
		t.Fatal("expected default shadow test run")
	}
}

func TestSaveDiffResult_matchAndMismatch(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	match := diff.Result{
		TraceID:  "trace-match",
		Protocol: diff.ProtocolIngress,
		Status:   diff.StatusMatch,
	}
	if err := db.SaveDiffResult(ctx, "demo", match); err != nil {
		t.Fatal(err)
	}

	mismatch := diff.Result{
		TraceID:  "trace-mismatch",
		Protocol: diff.ProtocolIngress,
		Status:   diff.StatusMismatch,
		BodyA:    []byte(`{"price":10}`),
		BodyC:    []byte(`{"price":12}`),
		Regressions: []diff.PathDiff{
			{Path: "price", Expected: "10", Actual: "12"},
		},
	}
	if err := db.SaveDiffResult(ctx, "demo", mismatch); err != nil {
		t.Fatal(err)
	}

	runs, err := db.ListShadowTests(ctx, 1)
	if err != nil || len(runs) == 0 {
		t.Fatal(err)
	}
	if runs[0].TotalTraces < 2 {
		t.Fatalf("total traces = %d", runs[0].TotalTraces)
	}
	if runs[0].MismatchCount != 1 {
		t.Fatalf("mismatch count = %d", runs[0].MismatchCount)
	}

	traces, err := db.ListTraces(ctx, runs[0].ID, "MISMATCH", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(traces) != 1 {
		t.Fatalf("mismatch traces = %d", len(traces))
	}
	mms, err := db.ListMismatchesForTrace(ctx, "trace-mismatch", "ingress")
	if err != nil || len(mms) != 1 {
		t.Fatalf("mismatches = %v err=%v", mms, err)
	}
	if !mms[0].BodyAJSON.Valid || !mms[0].BodyCJSON.Valid {
		t.Fatal("expected bodies on first mismatch row")
	}
}

func TestSaveDiffResult_egressPayloadSequence(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()

	mismatch := diff.Result{
		TraceID:  "trace-seq",
		Protocol: "mongodb",
		Status:   diff.StatusMismatch,
		ControlA: [][]byte{
			[]byte(`{"insert":"orders","documents":[{"order_id":"1"}]}`),
		},
		ControlB: [][]byte{
			[]byte(`{"insert":"orders","documents":[{"order_id":"1"}]}`),
		},
		Candidate: [][]byte{
			[]byte(`{"insert":"orders","documents":[{"order_id":"1"}]}`),
			[]byte(`{"insert":"orders","documents":[{"audit":"n1"}]}`),
		},
		BodyA: []byte(`{"insert":"orders"}`),
		BodyC: []byte(`{"insert":"orders"}`),
		Regressions: []diff.PathDiff{
			{Path: "(count)", Expected: "1", Actual: "2"},
		},
	}
	if err := db.SaveDiffResult(ctx, "demo", mismatch); err != nil {
		t.Fatal(err)
	}
	payloads, err := db.ListEgressPayloads(ctx, "trace-seq", "mongodb")
	if err != nil {
		t.Fatal(err)
	}
	if len(payloads) != 4 {
		t.Fatalf("payloads = %d, want 4 (1 control-a + 1 control-b + 2 candidate)", len(payloads))
	}
}
