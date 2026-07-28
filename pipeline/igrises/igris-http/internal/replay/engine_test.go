package replay

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shadow-diff/igris/internal/driver"
	"github.com/shadow-diff/igris/internal/payload"
	"github.com/shadow-diff/igris/internal/trace"
)

func TestEngineStartMulticastsTraceparentAndRole(t *testing.T) {
	t.Parallel()

	var (
		mu   sync.Mutex
		hits = map[string][]hit{}
	)
	record := func(role string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			_ = r.Body.Close()
			mu.Lock()
			hits[role] = append(hits[role], hit{
				method:      r.Method,
				path:        r.URL.RequestURI(),
				traceparent: r.Header.Get(trace.HeaderTraceparent),
				role:        r.Header.Get(headerShadowRole),
				body:        string(body),
			})
			mu.Unlock()
			w.WriteHeader(http.StatusOK)
		}
	}

	a := httptest.NewServer(record("control-a"))
	defer a.Close()
	b := httptest.NewServer(record("control-b"))
	defer b.Close()
	c := httptest.NewServer(record("candidate"))
	defer c.Close()

	eng := &Engine{
		Records: []driver.IngressCapture{
			{
				Traceparent: "00-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-bbbbbbbbbbbbbbbb-01",
				TraceID:     "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				Method:      "POST",
				Path:        "/orders",
				RequestURI:  "/orders?x=1",
				Headers:     map[string]string{"Content-Type": "application/json"},
				Body:        []byte(`{"ok":true}`),
			},
			{
				Traceparent: "00-cccccccccccccccccccccccccccccccc-dddddddddddddddd-01",
				Method:      "GET",
				Path:        "/health",
				RequestURI:  "/health",
			},
		},
		Targets: []payload.Target{
			{Name: "control-a", BaseURL: a.URL},
			{Name: "control-b", BaseURL: b.URL},
			{Name: "candidate", BaseURL: c.URL},
		},
		Client: a.Client(),
	}

	n, already := eng.Start()
	if already || n != 2 {
		t.Fatalf("Start() n=%d already=%v", n, already)
	}
	eng.Wait()

	mu.Lock()
	defer mu.Unlock()
	for _, role := range []string{"control-a", "control-b", "candidate"} {
		got := hits[role]
		if len(got) != 2 {
			t.Fatalf("%s hits=%d want 2: %+v", role, len(got), got)
		}
		if got[0].method != "POST" || got[0].path != "/orders?x=1" {
			t.Fatalf("%s first hit=%+v", role, got[0])
		}
		if got[0].traceparent != "00-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-bbbbbbbbbbbbbbbb-01" {
			t.Fatalf("%s missing traceparent: %+v", role, got[0])
		}
		if got[0].role != role {
			t.Fatalf("%s x-shadow-role=%q", role, got[0].role)
		}
		if got[1].method != "GET" || got[1].traceparent == "" {
			t.Fatalf("%s second hit=%+v", role, got[1])
		}
	}

	_, already = eng.Start()
	if already {
		t.Fatal("Start after Wait should not be already running")
	}
	eng.Wait()
}

func TestEngineStartConflictWhileRunning(t *testing.T) {
	t.Parallel()

	gate := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-gate
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	eng := &Engine{
		Records: []driver.IngressCapture{{Method: "GET", RequestURI: "/x"}},
		Targets: []payload.Target{{Name: "control-a", BaseURL: srv.URL}},
		Client:  srv.Client(),
	}
	if _, already := eng.Start(); already {
		t.Fatal("first Start should not be already")
	}
	// Busy-wait until running is visible.
	deadline := time.Now().Add(2 * time.Second)
	for !eng.Running() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if _, already := eng.Start(); !already {
		t.Fatal("second Start should report already running")
	}
	close(gate)
	eng.Wait()
}

type hit struct {
	method, path, traceparent, role, body string
}

func TestHandlerStartAcceptedAndConflict(t *testing.T) {
	t.Parallel()

	var entered atomic.Int32
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		entered.Add(1)
		<-block
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	eng := &Engine{
		Records: []driver.IngressCapture{{Method: "GET", RequestURI: "/y"}},
		Targets: []payload.Target{{Name: "control-a", BaseURL: srv.URL}},
		Client:  srv.Client(),
	}
	h := &Handler{Engine: eng}
	mux := http.NewServeMux()
	h.Mount(mux)

	rec1 := httptest.NewRecorder()
	mux.ServeHTTP(rec1, httptest.NewRequest(http.MethodPost, "/v1/replay/start", nil))
	if rec1.Code != http.StatusAccepted {
		t.Fatalf("first status=%d", rec1.Code)
	}

	deadline := time.Now().Add(2 * time.Second)
	for entered.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}

	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, httptest.NewRequest(http.MethodPost, "/v1/replay/start", nil))
	if rec2.Code != http.StatusConflict {
		t.Fatalf("second status=%d want 409", rec2.Code)
	}

	close(block)
	eng.Wait()
}
