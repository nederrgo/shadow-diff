package diff

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

func TestAnalyzeMongoEgress_noRegression(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	AnalyzeMongoEgress(log, "t1", []byte(`{"v":1}`), []byte(`{"v":1}`), []byte(`{"v":1}`))
	if !strings.Contains(buf.String(), "No egress regression for Trace t1 (mongodb)") {
		t.Fatalf("got: %s", buf.String())
	}
}

func TestAnalyzeEgress_rabbitmq(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	AnalyzeEgress(log, "t-rmq", "rabbitmq", []byte(`{"v":1}`), []byte(`{"v":1}`), []byte(`{"v":2}`))
	if !strings.Contains(buf.String(), "Egress regression for Trace t-rmq (rabbitmq)") {
		t.Fatalf("got: %s", buf.String())
	}
}

func TestAnalyzeMongoEgress_regression(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	AnalyzeMongoEgress(log, "t2", []byte(`{"v":1}`), []byte(`{"v":1}`), []byte(`{"v":2}`))
	if !strings.Contains(buf.String(), "Egress regression for Trace t2 (mongodb)") {
		t.Fatalf("got: %s", buf.String())
	}
}
