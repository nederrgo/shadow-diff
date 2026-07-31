package storage

import (
	"log/slog"
	"os"
	"strings"

	v2storage "github.com/shadow-diff/beru/internal/v2/storage"
)

// DriverPostgres selects the durable backend via DB_DRIVER.
const DriverPostgres = "postgres"

// Backend is the pair of stores Beru runs on, whichever driver provided them.
type Backend struct {
	Runs   RunStore
	Traces v2storage.TraceRepository
	Close  func() error
}

// OpenBackend picks the storage driver from DB_DRIVER.
//
// "postgres" opens the durable backend and no SQLite file at all. Anything else,
// including unset, keeps the SQLite default. A misconfigured Postgres is a boot
// failure rather than a silent downgrade — diff history that quietly stopped
// being durable is the outcome this backend exists to prevent.
func OpenBackend(log *slog.Logger) (*Backend, error) {
	if log == nil {
		log = slog.Default()
	}
	if strings.EqualFold(strings.TrimSpace(os.Getenv("DB_DRIVER")), DriverPostgres) {
		cfg, err := PostgresConfigFromEnv()
		if err != nil {
			return nil, err
		}
		store, err := OpenPostgres(log, cfg)
		if err != nil {
			return nil, err
		}
		return &Backend{Runs: store, Traces: store, Close: store.Close}, nil
	}

	db, err := Open(log)
	if err != nil {
		return nil, err
	}
	repo, err := v2storage.NewSQLiteRepository(db.SQL())
	if err != nil {
		db.Close()
		return nil, err
	}
	return &Backend{Runs: db, Traces: repo, Close: db.Close}, nil
}
