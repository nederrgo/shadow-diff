package s3utils

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

type memPutter struct {
	mu      sync.Mutex
	objects map[string][]byte
	fail    bool
}

func (m *memPutter) PutObject(_ context.Context, key string, body []byte, _ string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fail {
		return context.DeadlineExceeded
	}
	if m.objects == nil {
		m.objects = map[string][]byte{}
	}
	cp := make([]byte, len(body))
	copy(cp, body)
	m.objects[key] = cp
	return nil
}

func TestObjectKeyPrefix(t *testing.T) {
	t.Parallel()
	cfg := Config{
		Namespace: "default",
		TestName:  "my-test",
		SessionID: "run-1",
		DataType:  DataTypeIngress,
	}
	want := "shadow-diff/default/my-test/sessions/run-1/ingress/"
	if got := cfg.ObjectKeyPrefix(); got != want {
		t.Fatalf("prefix=%q want %q", got, want)
	}
}

func TestBatchUploaderFlushBySize(t *testing.T) {
	t.Parallel()
	putter := &memPutter{}
	u, err := NewBatchUploaderWithPutter(Config{
		Bucket:        "b",
		Namespace:     "ns",
		TestName:      "t",
		SessionID:     "s",
		DataType:      DataTypeEgress,
		FlushSize:     3,
		FlushInterval: time.Hour,
	}, putter, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = u.Close(context.Background()) }()

	for i := 0; i < 3; i++ {
		if err := u.Add(map[string]int{"n": i}); err != nil {
			t.Fatal(err)
		}
	}
	// Size-triggered flush is synchronous inside Add.
	putter.mu.Lock()
	n := len(putter.objects)
	putter.mu.Unlock()
	if n != 1 {
		t.Fatalf("objects=%d want 1", n)
	}
	for key, body := range putter.objects {
		if !strings.HasPrefix(key, "shadow-diff/ns/t/sessions/s/egress/") {
			t.Fatalf("key=%q", key)
		}
		if !strings.HasSuffix(key, ".jsonl") {
			t.Fatalf("key=%q missing .jsonl", key)
		}
		lines := strings.Split(strings.TrimSpace(string(body)), "\n")
		if len(lines) != 3 {
			t.Fatalf("lines=%d body=%q", len(lines), body)
		}
	}
}

func TestBatchUploaderFlushOnClose(t *testing.T) {
	t.Parallel()
	putter := &memPutter{}
	u, err := NewBatchUploaderWithPutter(Config{
		Bucket:        "b",
		Namespace:     "ns",
		TestName:      "t",
		SessionID:     "s",
		DataType:      DataTypeIngress,
		FlushSize:     100,
		FlushInterval: time.Hour,
	}, putter, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := u.Add(map[string]string{"traceparent": "00-aa-bb-01"}); err != nil {
		t.Fatal(err)
	}
	if err := u.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	putter.mu.Lock()
	defer putter.mu.Unlock()
	if len(putter.objects) != 1 {
		t.Fatalf("objects=%d want 1", len(putter.objects))
	}
}

func TestConfigValidate(t *testing.T) {
	t.Parallel()
	err := Config{DataType: "foo", Bucket: "b", Namespace: "n", TestName: "t", SessionID: "s"}.Validate()
	if err == nil {
		t.Fatal("expected error for bad dataType")
	}
}
