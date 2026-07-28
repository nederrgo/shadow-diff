package replay

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/shadow-diff/igris/internal/driver"
	"github.com/shadow-diff/s3utils"
)

// LoadIngress downloads session ingress JSONL and parses IngressCapture records in FIFO order.
func LoadIngress(ctx context.Context, reader *s3utils.S3Reader, log *slog.Logger) ([]driver.IngressCapture, error) {
	if reader == nil {
		return nil, fmt.Errorf("replay: S3Reader is required")
	}
	if log == nil {
		log = slog.Default()
	}
	lines, err := reader.ReadAllJSONL(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]driver.IngressCapture, 0, len(lines))
	for i, line := range lines {
		var rec driver.IngressCapture
		if err := json.Unmarshal(line, &rec); err != nil {
			log.Warn("skip malformed ingress JSONL line", "line", i+1, "err", err)
			continue
		}
		if rec.Method == "" {
			log.Warn("skip ingress JSONL line without method", "line", i+1)
			continue
		}
		if rec.RequestURI == "" && rec.Path == "" {
			log.Warn("skip ingress JSONL line without path", "line", i+1)
			continue
		}
		out = append(out, rec)
	}
	return out, nil
}
