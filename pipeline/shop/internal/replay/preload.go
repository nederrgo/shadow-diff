package replay

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/shadow-diff/s3utils"
)

// egressRecord matches the JSON shape written by Shop in OPERATING_MODE=record.
type egressRecord struct {
	TraceID  string `json:"trace_id"`
	Method   string `json:"method"`
	Host     string `json:"host"`
	Path     string `json:"path"`
	Response struct {
		Status  int               `json:"status"`
		Headers map[string]string `json:"headers"`
		Body    string            `json:"body"`
	} `json:"response"`
}

// PreloadFromS3 lists egress JSONL under the session prefix and seeds the mock store.
// Malformed lines are skipped; S3 list/get errors fail the call.
func PreloadFromS3(ctx context.Context, reader *s3utils.S3Reader, mocks *MockStore, log *slog.Logger) (int, error) {
	if reader == nil {
		return 0, fmt.Errorf("replay: S3Reader is required")
	}
	if mocks == nil {
		return 0, fmt.Errorf("replay: MockStore is required")
	}
	if log == nil {
		log = slog.Default()
	}
	lines, err := reader.ReadAllJSONL(ctx)
	if err != nil {
		return 0, err
	}
	loaded := 0
	for i, line := range lines {
		var rec egressRecord
		if err := json.Unmarshal(line, &rec); err != nil {
			log.Warn("skip malformed egress JSONL line", "line", i+1, "err", err)
			continue
		}
		if rec.TraceID == "" || rec.Method == "" || rec.Host == "" || rec.Path == "" || rec.Response.Status == 0 {
			log.Warn("skip incomplete egress JSONL line", "line", i+1)
			continue
		}
		key := TraceKey(rec.TraceID, rec.Method, HostWithoutPort(rec.Host), rec.Path)
		mocks.Put(key, EarlyResponse{
			StatusCode: rec.Response.Status,
			Headers:    rec.Response.Headers,
			Body:       []byte(rec.Response.Body),
		})
		loaded++
	}
	return loaded, nil
}
