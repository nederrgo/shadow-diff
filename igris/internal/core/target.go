package core

import "github.com/shadow-diff/igris/internal/payload"

// Target is a named multicast destination.
type Target = payload.Target

// Result holds the outcome of one outbound clone.
type Result = payload.Result

// ResultLogAttrs formats results for slog.
var ResultLogAttrs = payload.ResultLogAttrs
