package storage

import (
	"context"
	"testing"
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
