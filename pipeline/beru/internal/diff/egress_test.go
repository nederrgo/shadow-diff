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
	_, err := AnalyzeMongoEgress(log, "t1",
		[][]byte{[]byte(`{"insert":"c","documents":[{"id":1}]}`)},
		[][]byte{[]byte(`{"insert":"c","documents":[{"id":1}]}`)},
		[][]byte{[]byte(`{"insert":"c","documents":[{"id":1}]}`)},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "No egress regression for Trace t1 (mongodb)") {
		t.Fatalf("got: %s", buf.String())
	}
}

func TestAnalyzeEgress_rabbitmq(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	_, err := AnalyzeEgress(log, "t-rmq", "rabbitmq",
		[][]byte{[]byte(`{"v":1}`)},
		[][]byte{[]byte(`{"v":1}`)},
		[][]byte{[]byte(`{"v":2}`)},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "Egress regression for Trace t-rmq (rabbitmq)") {
		t.Fatalf("got: %s", buf.String())
	}
}

func TestAnalyzeMongoEgress_regression(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	_, err := AnalyzeMongoEgress(log, "t2",
		[][]byte{[]byte(`{"insert":"c","documents":[{"id":1}]}`)},
		[][]byte{[]byte(`{"insert":"c","documents":[{"id":1}]}`)},
		[][]byte{[]byte(`{"insert":"c","documents":[{"id":2}]}`)},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "Egress regression for Trace t2 (mongodb)") {
		t.Fatalf("got: %s", buf.String())
	}
}

func TestAnalyzeEgress_countRegression_mongodb(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	insert := []byte(`{"insert":"orders","documents":[{"order_id":"1"}]}`)
	extra := []byte(`{"insert":"orders","documents":[{"audit":"n1"}]}`)
	res, err := AnalyzeEgress(log, "t-count", "mongodb",
		[][]byte{insert},
		[][]byte{insert},
		[][]byte{insert, extra},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	want := "Egress count regression for Trace t-count (mongodb): expected 1 query but got 2"
	if !strings.Contains(buf.String(), want) {
		t.Fatalf("expected %q in logs, got: %s", want, buf.String())
	}
	if res.Status != StatusMismatch {
		t.Fatalf("status = %q", res.Status)
	}
}

func TestAnalyzeEgress_countRegression_rabbitmq(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	msg := []byte(`{"exchange":"orders","routing_key":"order.created","body":{}}`)
	_, err := AnalyzeEgress(log, "t-rmq-count", "rabbitmq",
		[][]byte{msg},
		[][]byte{msg},
		[][]byte{msg, msg},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "expected 1 message but got 2") {
		t.Fatalf("got: %s", buf.String())
	}
}

func TestAnalyzeEgress_signaturePairing_outOfOrder(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	insert := []byte(`{"insert":"orders","documents":[{"order_id":"1"}]}`)
	update := []byte(`{"update":"status","updates":[{"$set":{"s":"ok"}}]}`)
	_, err := AnalyzeEgress(log, "t-order", "mongodb",
		[][]byte{insert, update},
		[][]byte{insert, update},
		[][]byte{update, insert},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "Egress regression") {
		t.Fatalf("expected no regressions for out-of-order matching signatures, got: %s", buf.String())
	}
	if !strings.Contains(buf.String(), "No egress regression for Trace t-order (mongodb)") {
		t.Fatalf("got: %s", buf.String())
	}
}

func TestAnalyzeEgress_extraSignature_noCascade(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	insert := []byte(`{"insert":"orders","documents":[{"order_id":"1"}]}`)
	update := []byte(`{"update":"status","updates":[{"$set":{"s":"ok"}}]}`)
	extra := []byte(`{"insert":"audit","documents":[{"audit":"n1"}]}`)
	_, err := AnalyzeEgress(log, "t-extra", "mongodb",
		[][]byte{insert, update},
		[][]byte{insert, update},
		[][]byte{insert, extra, update},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	want := "Egress count regression for Trace t-extra (mongodb): expected 2 queries but got 3"
	if !strings.Contains(buf.String(), want) {
		t.Fatalf("expected count regression (same count path), got: %s", buf.String())
	}
}

func TestGenerateEgressSignature_mongodb(t *testing.T) {
	sig := generateEgressSignature("mongodb", []byte(`{"insert":"orders","documents":[]}`))
	if sig != "mongodb:insert:orders" {
		t.Fatalf("got %q", sig)
	}
}

func TestGenerateEgressSignature_rabbitmq(t *testing.T) {
	sig := generateEgressSignature("rabbitmq", []byte(`{"exchange":"orders","routing_key":"order.created"}`))
	if sig != "rabbitmq:publish:orders:order.created" {
		t.Fatalf("got %q", sig)
	}
}
