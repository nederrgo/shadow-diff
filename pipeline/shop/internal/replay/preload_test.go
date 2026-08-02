package replay

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/shadow-diff/s3utils"
)

type memGetter struct {
	objects map[string][]byte
}

func (m *memGetter) ListKeys(_ context.Context, prefix string) ([]string, error) {
	var keys []string
	for k := range m.objects {
		if strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	return keys, nil
}

func (m *memGetter) GetObject(_ context.Context, key string) ([]byte, error) {
	b, ok := m.objects[key]
	if !ok {
		return nil, fmt.Errorf("key not found: %s", key)
	}
	return b, nil
}

func TestPreloadFromS3(t *testing.T) {
	t.Parallel()
	prefix := "shadow-diff/ns/t/sessions/s/egress/"
	line := `{"trace_id":"abc","method":"GET","host":"api.example.com:443","path":"/v1","response":{"status":200,"headers":{"Content-Type":"application/json"},"body":"{}"}}`
	bad := `not-json`
	store := &memGetter{objects: map[string][]byte{
		prefix + "1.jsonl": []byte(line + "\n" + bad + "\n"),
	}}
	reader, err := s3utils.NewS3ReaderWithGetter(s3utils.Config{
		Bucket:    "b",
		Namespace: "ns",
		TestName:  "t",
		SessionID: "s",
		DataType:  s3utils.DataTypeEgress,
	}, store)
	if err != nil {
		t.Fatal(err)
	}
	mocks := NewMockStore()
	n, err := PreloadFromS3(context.Background(), reader, mocks, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("loaded=%d want 1", n)
	}
	key := TraceKey("abc", "GET", HostWithoutPort("api.example.com:443"), "/v1")
	resp, ok := mocks.Get(key)
	if !ok || resp.StatusCode != 200 {
		t.Fatalf("mock missing or wrong: ok=%v status=%d", ok, resp.StatusCode)
	}
}
