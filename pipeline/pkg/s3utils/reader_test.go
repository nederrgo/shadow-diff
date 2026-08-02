package s3utils

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

type memStore struct {
	objects map[string][]byte
}

func (m *memStore) ListKeys(_ context.Context, prefix string) ([]string, error) {
	var keys []string
	for k := range m.objects {
		if strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	return keys, nil
}

func (m *memStore) GetObject(_ context.Context, key string) ([]byte, error) {
	b, ok := m.objects[key]
	if !ok {
		return nil, fmt.Errorf("key not found: %s", key)
	}
	cp := make([]byte, len(b))
	copy(cp, b)
	return cp, nil
}

func TestS3ReaderReadAllJSONL(t *testing.T) {
	t.Parallel()
	prefix := "shadow-diff/ns/t/sessions/s/egress/"
	store := &memStore{objects: map[string][]byte{
		prefix + "1.jsonl": []byte("{\"n\":1}\n{\"n\":2}\n"),
		prefix + "2.jsonl": []byte("{\"n\":3}\n\n"),
		prefix + "skip.txt": []byte("ignore"),
		prefix + "dir/":     []byte(""),
	}}
	r, err := NewS3ReaderWithGetter(Config{
		Bucket:    "b",
		Namespace: "ns",
		TestName:  "t",
		SessionID: "s",
		DataType:  DataTypeEgress,
	}, store)
	if err != nil {
		t.Fatal(err)
	}
	lines, err := r.ReadAllJSONL(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 3 {
		t.Fatalf("lines=%d want 3", len(lines))
	}
}

func TestS3ReaderListJSONLKeysSorted(t *testing.T) {
	t.Parallel()
	prefix := "shadow-diff/ns/t/sessions/s/ingress/"
	store := &memStore{objects: map[string][]byte{
		prefix + "z.jsonl": []byte("{}\n"),
		prefix + "a.jsonl": []byte("{}\n"),
		prefix + "m.jsonl": []byte("{}\n"),
	}}
	r, err := NewS3ReaderWithGetter(Config{
		Bucket:    "b",
		Namespace: "ns",
		TestName:  "t",
		SessionID: "s",
		DataType:  DataTypeIngress,
	}, store)
	if err != nil {
		t.Fatal(err)
	}
	keys, err := r.ListJSONLKeys(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{prefix + "a.jsonl", prefix + "m.jsonl", prefix + "z.jsonl"}
	if len(keys) != 3 || keys[0] != want[0] || keys[1] != want[1] || keys[2] != want[2] {
		t.Fatalf("keys=%v want %v", keys, want)
	}
}
