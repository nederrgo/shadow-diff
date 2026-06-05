package storage

import (
	"context"
	"testing"

	"github.com/shadow-diff/beru/internal/diff"
)

func TestNoiseFilter_roundTrip(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	if err := db.AddNoiseFilter(ctx, "demo", "timestamp"); err != nil {
		t.Fatal(err)
	}
	paths, err := db.NoisePathsForTest(ctx, "demo")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := paths["timestamp"]; !ok {
		t.Fatalf("paths = %v", paths)
	}
}

func TestNoiseFilter_suppressesRegression(t *testing.T) {
	bodyA := []byte(`{"price":10,"timestamp":"t1"}`)
	bodyB := []byte(`{"price":10,"timestamp":"t2"}`)
	bodyC := []byte(`{"price":12,"timestamp":"t1"}`)

	noise, _ := diff.NoisePaths(bodyA, bodyB)
	user := map[string]struct{}{"price": {}}
	merged := diff.MergeNoise(noise, user)
	regs, err := diff.Regressions(bodyA, bodyC, merged)
	if err != nil {
		t.Fatal(err)
	}
	if len(regs) != 0 {
		t.Fatalf("expected price suppressed, got %v", regs)
	}
}
