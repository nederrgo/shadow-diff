package health

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHandle_NotReadyThenReady(t *testing.T) {
	s := &Server{}
	req := httptest.NewRequest(http.MethodGet, Path, nil)

	rr := httptest.NewRecorder()
	s.handle(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("before MarkReady: status=%d want 503", rr.Code)
	}

	s.MarkReady()
	rr = httptest.NewRecorder()
	s.handle(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("after MarkReady: status=%d want 200", rr.Code)
	}
	body, _ := io.ReadAll(rr.Body)
	if string(body) != "ok\n" {
		t.Fatalf("body=%q", body)
	}
}
