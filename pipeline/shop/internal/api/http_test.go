package api

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/shadow-diff/s3utils"
)

func TestRecordEgressReturnsHash(t *testing.T) {
	uploader, err := s3utils.NewBatchUploaderWithPutter(s3utils.Config{
		Bucket:        "b",
		Namespace:     "ns",
		TestName:      "t",
		SessionID:     "s",
		DataType:      s3utils.DataTypeEgress,
		FlushSize:     100,
		FlushInterval: 0, // default
	}, nopPutter{}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = uploader.Close(context.Background())
	})

	srv := &Server{Log: slog.Default(), Uploader: uploader}
	body := []byte(`{
		"trace_id":"abcd",
		"method":"GET",
		"host":"dep:8080",
		"path":"/dep/echo",
		"response":{"status":200,"body":"ok"}
	}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/record_egress", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	srv.handleRecordEgress(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body=%s", rr.Code, rr.Body.String())
	}
	var out struct {
		Hash string `json:"hash"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	want := "trace:abcd:GET:dep:/dep/echo"
	if out.Hash != want {
		t.Fatalf("hash = %q, want %q", out.Hash, want)
	}
}

type nopPutter struct{}

func (nopPutter) PutObject(ctx context.Context, key string, body []byte, contentType string) error {
	return nil
}
