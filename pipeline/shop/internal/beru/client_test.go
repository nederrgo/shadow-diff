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
	t.Parallel()
	var got Report
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/egress/diff" {
			t.Errorf("path = %q", r.URL.Path)
		}
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Errorf("unmarshal: %v", err)
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	client := NewClient(srv.URL)
	err := client.PostReport(context.Background(), Report{
		TraceID:  "abc",
		Workload: "control-a",
		Protocol: "http",
		Payload:  json.RawMessage(`{"method":"GET","path":"/x","status":200,"body":""}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.TraceID != "abc" || got.Workload != "control-a" || got.Protocol != "http" {
		t.Fatalf("got %+v", got)
	}
}

func TestBuildHTTPEgressPayload(t *testing.T) {
	t.Parallel()
	raw, err := BuildHTTPEgressPayload("POST", "h", "/p", 201, []byte(`{"a":1}`))
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	body, ok := got["body"].(map[string]any)
	if !ok || int(body["a"].(float64)) != 1 {
		t.Fatalf("body: %v", got["body"])
	}

	raw, err = BuildHTTPEgressPayload("GET", "h", "/p", 200, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got["body"] != "" {
		t.Fatalf("empty body: %#v", got["body"])
	}
}
