package replay

import (
	"context"
	"log/slog"

	"github.com/shadow-diff/igris/internal/driver"
	pkgreplay "github.com/shadow-diff/replay"
	"github.com/shadow-diff/s3utils"
)

// LoadIngress downloads session ingress JSONL and parses IngressCapture records in FIFO order.
func LoadIngress(ctx context.Context, reader *s3utils.S3Reader, log *slog.Logger) ([]driver.IngressCapture, error) {
	return pkgreplay.LoadJSONL(ctx, reader, log, func(rec driver.IngressCapture) bool {
		if rec.Method == "" {
			return false
		}
		return rec.RequestURI != "" || rec.Path != ""
	})
}
