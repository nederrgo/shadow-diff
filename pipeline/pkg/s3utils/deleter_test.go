package s3utils

import (
	"context"
	"fmt"
	"testing"
)

func TestTestKeyPrefix(t *testing.T) {
	t.Parallel()
	got := TestKeyPrefix("default", "demo")
	want := "shadow-diff/default/demo/"
	if got != want {
		t.Fatalf("TestKeyPrefix = %q, want %q", got, want)
	}
}

type fakeBulkDeleter struct {
	keys    map[string]struct{}
	deleted []string
}

func (f *fakeBulkDeleter) ListKeys(_ context.Context, prefix string) ([]string, error) {
	var out []string
	for k := range f.keys {
		if len(k) >= len(prefix) && k[:len(prefix)] == prefix {
			out = append(out, k)
		}
	}
	return out, nil
}

func (f *fakeBulkDeleter) DeleteKeys(_ context.Context, keys []string) error {
	for _, k := range keys {
		delete(f.keys, k)
		f.deleted = append(f.deleted, k)
	}
	return nil
}

func TestDeletePrefixWith(t *testing.T) {
	t.Parallel()
	f := &fakeBulkDeleter{keys: map[string]struct{}{
		"shadow-diff/ns/t/sessions/s1/ingress/a.jsonl":    {},
		"shadow-diff/ns/t/sessions/s1/egress/b.jsonl":     {},
		"shadow-diff/other/x/sessions/s1/ingress/c.jsonl": {},
	}}
	prefix := TestKeyPrefix("ns", "t")
	if err := DeletePrefixWith(context.Background(), prefix, f); err != nil {
		t.Fatalf("DeletePrefixWith: %v", err)
	}
	if len(f.deleted) != 2 {
		t.Fatalf("deleted = %d, want 2: %v", len(f.deleted), f.deleted)
	}
	if _, ok := f.keys["shadow-diff/other/x/sessions/s1/ingress/c.jsonl"]; !ok {
		t.Fatal("deleted keys outside test prefix")
	}
}

func TestDeletePrefixWith_Empty(t *testing.T) {
	t.Parallel()
	f := &fakeBulkDeleter{keys: map[string]struct{}{}}
	if err := DeletePrefixWith(context.Background(), "shadow-diff/ns/t/", f); err != nil {
		t.Fatalf("empty prefix delete: %v", err)
	}
	if len(f.deleted) != 0 {
		t.Fatalf("deleted = %v", f.deleted)
	}
}

func TestDeletePrefixWith_NilDeleter(t *testing.T) {
	t.Parallel()
	err := DeletePrefixWith(context.Background(), "p/", nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if got := fmt.Sprint(err); got == "" {
		t.Fatal("empty error")
	}
}
