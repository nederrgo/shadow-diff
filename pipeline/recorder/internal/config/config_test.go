package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadDownstreams_fromFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "downstreams.json")
	if err := os.WriteFile(path, []byte(`[{"host":"api.example.com","ignore_paths":["$.ts"]}]`), 0o644); err != nil {
		t.Fatal(err)
	}
	ds, err := loadDownstreams(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(ds) != 1 || ds[0].Host != "api.example.com" {
		t.Fatalf("got %+v", ds)
	}
}

func TestLoadDownstreams_missingFile(t *testing.T) {
	ds, err := loadDownstreams(filepath.Join(t.TempDir(), "missing.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(ds) != 0 {
		t.Fatalf("got %+v", ds)
	}
}
