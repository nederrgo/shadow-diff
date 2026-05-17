package diff

import (
	"bytes"
	"log/slog"
	"testing"
)

func TestCompareJSON_noiseAndRegression(t *testing.T) {
	bodyA := []byte(`{"price":10.0,"timestamp":"t1"}`)
	bodyB := []byte(`{"price":10.0,"timestamp":"t2"}`)
	bodyC := []byte(`{"price":12.0,"timestamp":"t1"}`)

	noise, err := NoisePaths(bodyA, bodyB)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := noise["timestamp"]; !ok {
		t.Fatalf("expected timestamp in noise, got %v", noise)
	}

	regs, err := Regressions(bodyA, bodyC, noise)
	if err != nil {
		t.Fatal(err)
	}
	if len(regs) != 1 {
		t.Fatalf("expected 1 regression, got %d", len(regs))
	}
	if regs[0].Path != "price" {
		t.Fatalf("expected price path, got %s", regs[0].Path)
	}
}

func TestAnalyze_logsRegression(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	bodyA := []byte(`{"price":10.0,"timestamp":"t1"}`)
	bodyB := []byte(`{"price":10.0,"timestamp":"t2"}`)
	bodyC := []byte(`{"price":12.0,"timestamp":"t1"}`)
	Analyze(log, "123", bodyA, bodyB, bodyC, nil)
	out := buf.String()
	if !bytes.Contains([]byte(out), []byte("Regression found in Trace 123")) {
		t.Fatalf("missing regression log: %s", out)
	}
	if !bytes.Contains([]byte(out), []byte("price")) {
		t.Fatalf("missing price in log: %s", out)
	}
}
