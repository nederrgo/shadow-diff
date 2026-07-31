package storage

import "context"

// RunStore is the shadow-test / noise-filter half of Beru's persistence, the
// part the TraceRouter and the dashboard reach for. The trace half lives behind
// v2/storage.TraceRepository. Both are satisfied by *PostgresStore; *DB
// satisfies this one and pairs with *v2/storage.SQLiteRepository.
type RunStore interface {
	EnsureShadowTest(ctx context.Context, name string) error
	NoisePathsForTest(ctx context.Context, shadowTestName string) (map[string]struct{}, error)
	AddNoiseFilter(ctx context.Context, shadowTestName, path string) error
	ListNoiseFilters(ctx context.Context, shadowTestName string) ([]string, error)
	ListShadowTests(ctx context.Context, limit int) ([]ShadowTest, error)
	GetShadowTest(ctx context.Context, id int64) (ShadowTest, error)
	DefaultShadowTestName() string
}

var _ RunStore = (*DB)(nil)
