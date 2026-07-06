package report

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/shadow-diff/beru/internal/v2/storage"
)

func TestFromWireEnvelope_http(t *testing.T) {
	env := &NetworkEventEnvelope{
		TraceID:           "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
		PodRole:           "control-a",
		ShadowTestName:    "my-shadow",
		Protocol:          "http",
		Direction:         "egress",
		RawRequestPayload: `{"amount":1}`,
		RawResponsePayload: `{"ok":true}`,
		Metadata:          `{"method":"POST","path":"/v1/charges"}`,
	}
	raw, err := FromWireEnvelope(env)
	if err != nil {
		t.Fatal(err)
	}
	if raw.TraceID != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Fatalf("trace_id = %q", raw.TraceID)
	}
	if raw.Signature != "http:POST:/v1/charges" {
		t.Fatalf("signature = %q", raw.Signature)
	}
	if raw.Direction != storage.DirectionEgress {
		t.Fatalf("direction = %q", raw.Direction)
	}
}

func TestFromWireEnvelope_mongodb(t *testing.T) {
	env := &NetworkEventEnvelope{
		TraceID:           "4bf92f3577b34da6a3ce929d0e0e4736",
		PodRole:           "candidate",
		Protocol:          "mongodb",
		RawRequestPayload: `{"insert":"orders","documents":[{"id":1}]}`,
		Metadata:          `{"command":"insert","collection":"orders"}`,
	}
	raw, err := FromWireEnvelope(env)
	if err != nil {
		t.Fatal(err)
	}
	if raw.Signature != "mongodb:insert:orders" {
		t.Fatalf("signature = %q", raw.Signature)
	}
}

func TestMongoSpanFixtures_wireSignatures(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "otlp", "testdata", "mongo_spans.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixtures []struct {
		Name          string            `json:"name"`
		Attrs         map[string]string `json:"attrs"`
		WantSignature string            `json:"want_signature"`
	}
	if err := json.Unmarshal(data, &fixtures); err != nil {
		t.Fatal(err)
	}
	for i, fx := range fixtures {
		stmt := fx.Attrs["db.statement"]
		if stmt == "" {
			stmt = fx.Attrs["db.query.text"]
		}
		cmd := fx.Attrs["db.operation"]
		if cmd == "" {
			cmd = fx.Attrs["db.operation.name"]
		}
		coll := fx.Attrs["db.mongodb.collection"]
		if coll == "" {
			coll = fx.Attrs["db.collection.name"]
		}
		if cmd == "" && stmt != "" && stmt[0] != '{' {
			cmd = stmt
			stmt = ""
		}
		meta, _ := json.Marshal(mongoWireMeta{
			Command:    cmd,
			Collection: coll,
		})
		env := &NetworkEventEnvelope{
			TraceID:           formatHexTrace(i + 1),
			PodRole:           "control-a",
			Protocol:          "mongodb",
			RawRequestPayload: stmt,
			Metadata:          string(meta),
		}
		raw, err := FromWireEnvelope(env)
		if err != nil {
			t.Fatalf("%s: %v", fx.Name, err)
		}
		if raw.Signature != fx.WantSignature {
			t.Fatalf("%s: signature = %q, want %q", fx.Name, raw.Signature, fx.WantSignature)
		}
	}
}

func TestParseHTTPWireMetadata_fromRequestLine(t *testing.T) {
	method, path := parseHTTPWireMetadata("", "POST /v1/charges HTTP/1.1\n\n{}")
	if method != "POST" || path != "/v1/charges" {
		t.Fatalf("got %q %q", method, path)
	}
}

func formatHexTrace(n int) string {
	return fmt.Sprintf("%032x", n)
}
