package server

import (
	"context"

	"github.com/shadow-diff/tusk/pkg/db"
)

// SessionStore is the Postgres-backed surface HTTPServer needs for diffs.
// A nil Store on HTTPServer means control-plane-only mode.
type SessionStore interface {
	ListSessions(ctx context.Context, opts db.ListSessionsOpts) ([]db.Session, error)
	GetSessionDiffs(ctx context.Context, sessionID string) ([]db.SessionDiff, error)
	GetSignatureOccurrences(ctx context.Context, traceID, signature string) (db.SignatureOccurrences, error)
	SessionSummary(ctx context.Context, sessionID string) (db.SessionSummary, error)
}
