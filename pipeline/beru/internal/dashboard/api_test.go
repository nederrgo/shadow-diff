package dashboard

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/shadow-diff/beru/internal/diff"
	"github.com/shadow-diff/beru/internal/storage"
)

func testHandler(t *testing.T) *Handler {
	t.Helper()
	dir := t.TempDir()
	db, err := storage.OpenAt(slog.Default(), filepath.Join(dir, "dash.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	h, err := NewHandler(db, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestAPIShadowTests(t *testing.T) {
	h := testHandler(t)
	mux := http.NewServeMux()
	h.Register(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/shadow-tests", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
}

func TestAPINoiseFilters(t *testing.T) {
	h := testHandler(t)
	mux := http.NewServeMux()
	h.Register(mux)

	body, _ := json.Marshal(map[string]string{
		"shadow_test_name": "default",
		"path":             "timestamp",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/noise/filters", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
}

func TestDashboardIndex(t *testing.T) {
	h := testHandler(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/dashboard/", nil)
	h.handleIndex(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	body := rec.Body.Bytes()
	if !bytes.Contains(body, []byte("Beru Dashboard")) {
		t.Fatal("missing dashboard title")
	}
	if !bytes.Contains(body, []byte(`data-filter="all" class="filter-tab px-3 py-1 rounded text-sm active"`)) {
		t.Fatal("expected All tab active by default")
	}
}

func TestDashboardFilterTabs(t *testing.T) {
	h := testHandler(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/dashboard/?filter=match", nil)
	h.handleIndex(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	body := rec.Body.Bytes()
	if !bytes.Contains(body, []byte(`data-filter="match" class="filter-tab px-3 py-1 rounded text-sm active"`)) {
		t.Fatal("expected Matches tab active for filter=match")
	}
	if bytes.Contains(body, []byte(`data-filter="all" class="filter-tab px-3 py-1 rounded text-sm active"`)) {
		t.Fatal("expected All tab inactive when filter=match")
	}
}

func TestSaveAndViewTrace(t *testing.T) {
	h := testHandler(t)
	ctx := t.Context()
	res := diff.Result{
		TraceID:  "view-trace",
		Protocol: diff.ProtocolIngress,
		Status:   diff.StatusMismatch,
		BodyA:    []byte(`{"x":1}`),
		BodyC:    []byte(`{"x":2}`),
		Regressions: []diff.PathDiff{{Path: "x", Expected: "1", Actual: "2"}},
	}
	if err := h.DB.SaveDiffResult(ctx, "default", res); err != nil {
		t.Fatal(err)
	}
	runs, err := h.DB.ListShadowTests(ctx, 1)
	if err != nil || len(runs) == 0 {
		t.Fatal(err)
	}
	traces, err := h.DB.ListTraces(ctx, runs[0].ID, "", 10)
	if err != nil || len(traces) == 0 {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/dashboard/traces/"+strconv.FormatInt(traces[0].ID, 10), nil)
	h.handleTrace(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
}
