package server

import (
	"context"

	"github.com/shadow-diff/tusk/pkg/db"
)

// SessionStore is the Postgres-backed surface HTTPServer needs for diffs.
// A nil Store on HTTPServer means control-plane-only mode.
type SessionStore interface {
	ListSessions(ctx context.Context, opts db.ListSessionsOpts) ([]db.Session, error)
	ListExecutions(ctx context.Context, sessionID string) ([]db.ReplayExecution, error)
	GetSessionDiffs(ctx context.Context, sessionID, execID string) ([]db.SessionDiff, error)
	GetSignatureOccurrences(ctx context.Context, traceID, signature, execID string) (db.SignatureOccurrences, error)
	SessionSummary(ctx context.Context, sessionID, execID string) (db.SessionSummary, error)
	ResolveExecutionID(ctx context.Context, sessionID, execID string) (string, error)
}
