package replay

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

type fakeStarter struct {
	total   int
	already bool
	calls   atomic.Int32
}

func (f *fakeStarter) Start() (int, bool) {
	f.calls.Add(1)
	return f.total, f.already
}

func TestAdmin_startAccepted(t *testing.T) {
	t.Parallel()
	h := &Handler{Engine: &fakeStarter{total: 3}}
	mux := http.NewServeMux()
	h.Mount(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/replay/start", nil))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status=%d", rec.Code)
	}
}

func TestAdmin_conflict(t *testing.T) {
	t.Parallel()
	h := &Handler{Engine: &fakeStarter{total: 3, already: true}}
	mux := http.NewServeMux()
	h.Mount(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/replay/start", nil))
	if rec.Code != http.StatusConflict {
		t.Fatalf("status=%d", rec.Code)
	}
}

func TestEngine_StartOnce(t *testing.T) {
	t.Parallel()
	var n atomic.Int32
	e := &Engine[int]{
		Records: []int{1, 2},
		Dispatch: func(_ context.Context, _ int) {
			n.Add(1)
		},
	}
	total, already := e.Start()
	if already || total != 2 {
		t.Fatalf("total=%d already=%v", total, already)
	}
	_, already = e.Start()
	if !already {
		t.Fatal("second Start should conflict")
	}
	e.Wait()
	if n.Load() != 2 {
		t.Fatalf("dispatched=%d", n.Load())
	}
}
