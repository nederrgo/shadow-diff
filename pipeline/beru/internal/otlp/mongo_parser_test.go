package otlp

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseMongoStatement_validInsert(t *testing.T) {
	got, err := ParseMongoStatement(`{"insert": "orders", "documents": [{"id": 123}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(got) {
		t.Fatalf("invalid json: %s", got)
	}
	var m map[string]any
	if err := json.Unmarshal(got, &m); err != nil {
		t.Fatal(err)
	}
	if m["insert"] != "orders" {
		t.Fatalf("insert = %v", m["insert"])
	}
}

func TestParseMongoStatement_nonJSONWrapped(t *testing.T) {
	got, err := ParseMongoStatement("not-json-at-all")
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]string
	if err := json.Unmarshal(got, &m); err != nil {
		t.Fatal(err)
	}
	if m["query"] != "not-json-at-all" {
		t.Fatalf("query = %q", m["query"])
	}
}

func TestParseMongoStatement_oidNormalized(t *testing.T) {
	got, err := ParseMongoStatement(`{"insert":"c","documents":[{"_id":{"$oid":"507f1f77bcf86cd799439011"}}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(got) {
		t.Fatalf("invalid json: %s", got)
	}
	s := string(got)
	if strings.Contains(s, "$oid") {
		t.Fatalf("expected $oid normalized away, got %s", s)
	}
	if !strings.Contains(s, "507f1f77bcf86cd799439011") {
		t.Fatalf("expected oid value preserved, got %s", s)
	}
}
