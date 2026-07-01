package dashboard

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	v2storage "github.com/shadow-diff/beru/internal/v2/storage"
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
	repo, err := v2storage.NewSQLiteRepository(db.SQL())
	if err != nil {
		t.Fatal(err)
	}
	h, err := NewHandler(db, repo, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func seedMongoMismatch(t *testing.T, h *Handler, traceID string) {
	t.Helper()
	ctx := t.Context()
	repo := h.Repo
	now := time.Now().UTC()
	for _, rep := range []v2storage.RawReport{
		{TraceID: traceID, ShadowRole: "control-a", ShadowTestName: "default", Protocol: "mongodb", Direction: v2storage.DirectionEgress, Signature: "mongodb:insert:orders", PayloadBytes: []byte(`{"insert":"orders","documents":[{"order_id":"1"}]}`), CapturedAt: now},
		{TraceID: traceID, ShadowRole: "control-b", ShadowTestName: "default", Protocol: "mongodb", Direction: v2storage.DirectionEgress, Signature: "mongodb:insert:orders", PayloadBytes: []byte(`{"insert":"orders","documents":[{"order_id":"1"}]}`), CapturedAt: now},
		{TraceID: traceID, ShadowRole: "candidate", ShadowTestName: "default", Protocol: "mongodb", Direction: v2storage.DirectionEgress, Signature: "mongodb:insert:orders", PayloadBytes: []byte(`{"insert":"orders","documents":[{"order_id":"1"}]}`), CapturedAt: now},
		{TraceID: traceID, ShadowRole: "candidate", ShadowTestName: "default", Protocol: "mongodb", Direction: v2storage.DirectionEgress, Signature: "mongodb:insert:orders", PayloadBytes: []byte(`{"insert":"orders","documents":[{"audit":"n1"}]}`), CapturedAt: now.Add(time.Millisecond)},
	} {
		if _, err := repo.AppendReport(ctx, &rep); err != nil {
			t.Fatal(err)
		}
	}
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
}

func TestSaveAndViewTraceSequence(t *testing.T) {
	h := testHandler(t)
	traceID := "view-seq"
	seedMongoMismatch(t, h, traceID)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/dashboard/traces/"+traceID+"?protocol=mongodb", nil)
	h.handleTrace(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.Bytes()
	if !bytes.Contains(body, []byte("Egress sequence")) {
		t.Fatal("missing egress sequence section")
	}
	if !bytes.Contains(body, []byte("Unexpected extra egress")) {
		t.Fatal("missing extra egress badge")
	}
	if !bytes.Contains(body, []byte("mongodb:insert:orders")) {
		t.Fatal("missing egress signature")
	}
}

func TestSaveAndViewTraceIngress(t *testing.T) {
	h := testHandler(t)
	ctx := t.Context()
	traceID := "view-trace"
	now := time.Now().UTC()
	for _, rep := range []v2storage.RawReport{
		{TraceID: traceID, ShadowRole: "control-a", ShadowTestName: "default", Protocol: "http", Direction: v2storage.DirectionIngress, Signature: "http:GET:/", PayloadBytes: []byte(`{"x":1}`), CapturedAt: now},
		{TraceID: traceID, ShadowRole: "control-b", ShadowTestName: "default", Protocol: "http", Direction: v2storage.DirectionIngress, Signature: "http:GET:/", PayloadBytes: []byte(`{"x":1}`), CapturedAt: now},
		{TraceID: traceID, ShadowRole: "candidate", ShadowTestName: "default", Protocol: "http", Direction: v2storage.DirectionIngress, Signature: "http:GET:/", PayloadBytes: []byte(`{"x":2}`), CapturedAt: now},
	} {
		if _, err := h.Repo.AppendReport(ctx, &rep); err != nil {
			t.Fatal(err)
		}
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/dashboard/traces/"+traceID+"?protocol=http&direction=ingress", nil)
	h.handleTrace(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.Bytes()
	if !bytes.Contains(body, []byte("Ingress response")) {
		t.Fatal("missing ingress response section")
	}
}

func TestListTraceSummariesHTTPSplitByDirection(t *testing.T) {
	h := testHandler(t)
	ctx := t.Context()
	traceID := "http-split"
	now := time.Now().UTC()
	for _, rep := range []v2storage.RawReport{
		{TraceID: traceID, ShadowRole: "control-a", ShadowTestName: "default", Protocol: "http", Direction: v2storage.DirectionIngress, Signature: "http:POST:/work", PayloadBytes: []byte(`{"ok":true}`), CapturedAt: now},
		{TraceID: traceID, ShadowRole: "control-b", ShadowTestName: "default", Protocol: "http", Direction: v2storage.DirectionIngress, Signature: "http:POST:/work", PayloadBytes: []byte(`{"ok":true}`), CapturedAt: now},
		{TraceID: traceID, ShadowRole: "candidate", ShadowTestName: "default", Protocol: "http", Direction: v2storage.DirectionIngress, Signature: "http:POST:/work", PayloadBytes: []byte(`{"ok":true}`), CapturedAt: now},
		{TraceID: traceID, ShadowRole: "control-a", ShadowTestName: "default", Protocol: "http", Direction: v2storage.DirectionEgress, Signature: "http:GET:/api", PayloadBytes: []byte(`{"out":1}`), CapturedAt: now},
		{TraceID: traceID, ShadowRole: "control-b", ShadowTestName: "default", Protocol: "http", Direction: v2storage.DirectionEgress, Signature: "http:GET:/api", PayloadBytes: []byte(`{"out":1}`), CapturedAt: now},
		{TraceID: traceID, ShadowRole: "candidate", ShadowTestName: "default", Protocol: "http", Direction: v2storage.DirectionEgress, Signature: "http:GET:/api", PayloadBytes: []byte(`{"out":1}`), CapturedAt: now},
	} {
		if _, err := h.Repo.AppendReport(ctx, &rep); err != nil {
			t.Fatal(err)
		}
	}

	summaries, err := listTraceSummaries(ctx, h.Repo, "default", "", 50)
	if err != nil {
		t.Fatal(err)
	}
	var ingress, egress bool
	for _, s := range summaries {
		if s.TraceID != traceID || s.Protocol != "http" {
			continue
		}
		switch s.Direction {
		case v2storage.DirectionIngress:
			ingress = true
			if s.Signatures != "http:POST:/work" {
				t.Fatalf("ingress signatures = %q", s.Signatures)
			}
		case v2storage.DirectionEgress:
			egress = true
			if s.Signatures != "http:GET:/api" {
				t.Fatalf("egress signatures = %q", s.Signatures)
			}
		}
	}
	if !ingress || !egress {
		t.Fatalf("want ingress and egress rows, got ingress=%v egress=%v summaries=%+v", ingress, egress, summaries)
	}
}
