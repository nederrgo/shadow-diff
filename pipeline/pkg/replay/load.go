package replay

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/shadow-diff/s3utils"
)

// LoadJSONL downloads session JSONL and unmarshals records, skipping lines that
// fail accept (or fail to unmarshal).
func LoadJSONL[T any](ctx context.Context, reader *s3utils.S3Reader, log *slog.Logger, accept func(T) bool) ([]T, error) {
	if reader == nil {
		return nil, fmt.Errorf("replay: S3Reader is required")
	}
	if log == nil {
		log = slog.Default()
	}
	if accept == nil {
		accept = func(T) bool { return true }
	}
	lines, err := reader.ReadAllJSONL(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]T, 0, len(lines))
	for i, line := range lines {
		var rec T
		if err := json.Unmarshal(line, &rec); err != nil {
			log.Warn("skip malformed JSONL line", "line", i+1, "err", err)
			continue
		}
		if !accept(rec) {
			log.Warn("skip rejected JSONL line", "line", i+1)
			continue
		}
		out = append(out, rec)
	}
	return out, nil
}
