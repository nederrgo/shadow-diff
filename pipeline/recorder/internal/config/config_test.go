package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadRecordAndReplay_fromFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "recordAndReplay.json")
	if err := os.WriteFile(path, []byte(`[{"host":"api.example.com","ignore_paths":["$.ts"]}]`), 0o644); err != nil {
		t.Fatal(err)
	}
	hosts, err := loadRecordAndReplay(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(hosts) != 1 || hosts[0].Host != "api.example.com" {
		t.Fatalf("hosts = %+v", hosts)
	}
}

func TestLoadRecordAndReplay_missingFile(t *testing.T) {
	hosts, err := loadRecordAndReplay(filepath.Join(t.TempDir(), "missing.json"))
	if err != nil {
		t.Fatal(err)
	}
	if hosts != nil {
		t.Fatalf("expected nil, got %+v", hosts)
	}
}
