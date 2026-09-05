package beruclient

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestPostReport(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	var got Report
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != EgressDiffPath {
			t.Errorf("path = %q", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode: %v", err)
		}
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = io.WriteString(w, `{}`)
	}))
	defer srv.Close()

	client := NewClient(srv.URL)
	err := client.PostReport(context.Background(), Report{
		TraceID: "abc", Workload: "control-a", Protocol: "http",
		Signature: "http:GET:/x", ShadowTestName: "my-test",
		Payload: json.RawMessage(`{"method":"GET","path":"/x"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatalf("calls = %d, want 2", calls.Load())
	}
	if got.TraceID != "abc" || got.Signature != "http:GET:/x" || got.ShadowTestName != "my-test" {
		t.Fatalf("got %+v", got)
	}
}

func TestPostReportDoesNotRetry4xx(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()

	err := NewClient(srv.URL).PostReport(context.Background(), Report{})
	if err == nil || calls.Load() != 1 {
		t.Fatalf("err = %v, calls = %d; want error and one call", err, calls.Load())
	}
}

func TestRetryable(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"503 is retryable", &StatusError{Code: 503}, true},
		{"500 is retryable", &StatusError{Code: 500}, true},
		{"400 is not", &StatusError{Code: 400}, false},
		{"404 is not", &StatusError{Code: 404}, false},
		{"timeout is not", timeoutError{}, false},
	}
	for _, tt := range tests {
		if got := Retryable(tt.err); got != tt.want {
			t.Fatalf("%s: Retryable(%v) = %v want %v", tt.name, tt.err, got, tt.want)
		}
	}
}

type timeoutError struct{}

func (timeoutError) Error() string   { return "i/o timeout" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }
