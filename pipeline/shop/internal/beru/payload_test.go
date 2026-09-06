package beru

import (
	"encoding/json"
	"testing"
)

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
