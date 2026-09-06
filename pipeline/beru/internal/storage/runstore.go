package storage

import "context"

// ShadowTest is one shadow test run record.
type ShadowTest struct {
	ID            int64
	Name          string
	StartTime     string
	TotalTraces   int
	MismatchCount int
	MatchRate     float64
}

// RunStore is the shadow-test / noise-filter half of Beru's persistence, the
// part the TraceRouter reaches for. The trace half lives behind
// model.TraceRepository. Both are satisfied by *PostgresStore and *WALStore.
type RunStore interface {
	EnsureShadowTest(ctx context.Context, name string) error
	NoisePathsForTest(ctx context.Context, shadowTestName string) (map[string]struct{}, error)
	AddNoiseFilter(ctx context.Context, shadowTestName, path string) error
	ListNoiseFilters(ctx context.Context, shadowTestName string) ([]string, error)
	ListShadowTests(ctx context.Context, limit int) ([]ShadowTest, error)
	GetShadowTest(ctx context.Context, id int64) (ShadowTest, error)
	DefaultShadowTestName() string
}
