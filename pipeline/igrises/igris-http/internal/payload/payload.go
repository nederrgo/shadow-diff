package payload

import "context"

// Target is a named multicast destination.
type Target struct {
	Name    string
	BaseURL string
}

// Result holds the outcome of one outbound clone.
type Result struct {
	Name       string
	StatusCode int
	Err        error
}

// MulticastMessage delivers transformed traffic to all shadow targets.
type MulticastMessage interface {
	Dispatch(ctx context.Context, targets []Target) []Result
}

// ResultLogAttrs formats results for slog.
func ResultLogAttrs(results []Result) []any {
	attrs := make([]any, 0, len(results)*3)
	for _, r := range results {
		attrs = append(attrs, "target", r.Name)
		if r.Err != nil {
			attrs = append(attrs, "error", r.Err.Error())
			continue
		}
		attrs = append(attrs, "status_code", r.StatusCode)
	}
	return attrs
}
