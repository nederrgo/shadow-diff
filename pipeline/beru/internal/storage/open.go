package storage

import (
	"fmt"
	"log/slog"

	v2storage "github.com/shadow-diff/beru/internal/v2/storage"
)

// Backend is the pair of stores Beru runs on.
type Backend struct {
	Runs   RunStore
	Traces v2storage.TraceRepository
	Close  func() error
}

// OpenBackend opens PostgreSQL (required) and wraps it with the disk WAL flusher.
// Missing DB_HOST / DB_USER / DB_NAME fails the boot — there is no SQLite fallback.
func OpenBackend(log *slog.Logger) (*Backend, error) {
	if log == nil {
		log = slog.Default()
	}
	cfg, err := PostgresConfigFromEnv()
	if err != nil {
		return nil, err
	}
	store, err := OpenPostgres(log, cfg)
	if err != nil {
		return nil, err
	}
	wal, err := WrapWithWAL(log, store)
	if err != nil {
		_ = store.Close()
		return nil, fmt.Errorf("open wal: %w", err)
	}
	return &Backend{Runs: wal, Traces: wal, Close: wal.Close}, nil
}
