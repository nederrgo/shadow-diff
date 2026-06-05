package beru

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPostReport(t *testing.T) {
	var got Report
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/egress/diff" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatal(err)
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	client := NewClient(srv.URL + "/api/v1/egress/diff")
	err := client.PostReport(context.Background(), Report{
		TraceID:  "abc",
		Workload: "control-a",
		Protocol: "rabbitmq",
		Payload:  json.RawMessage(`{"k":1}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.TraceID != "abc" || got.Workload != "control-a" || got.Protocol != "rabbitmq" {
		t.Fatalf("unexpected report: %#v", got)
	}
}
