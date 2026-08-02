package db

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
)

const verdictChannel = "verdict_events"

const (
	listenMinBackoff = time.Second
	listenMaxBackoff = 30 * time.Second
)

// VerdictEvent is the JSON payload Beru puts on pg_notify('verdict_events', …).
type VerdictEvent struct {
	SessionID string `json:"session_id"`
	TraceID   string `json:"trace_id"`
	Verdict   string `json:"verdict"`
}

// EventHandler receives parsed NOTIFY payloads.
type EventHandler func(VerdictEvent)

// Listen opens a dedicated pgx connection, LISTENs on verdict_events, and
// forwards notifications until ctx is cancelled. Reconnects with capped backoff.
func Listen(ctx context.Context, dsn string, log *slog.Logger, handler EventHandler) error {
	if log == nil {
		log = slog.Default()
	}
	backoff := listenMinBackoff
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := listenOnce(ctx, dsn, log, handler)
		if err == nil || ctx.Err() != nil {
			return ctx.Err()
		}
		log.Warn("Postgres LISTEN disconnected; reconnecting", "err", err, "backoff", backoff)
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
		backoff = min(backoff*2, listenMaxBackoff)
	}
}

func listenOnce(ctx context.Context, dsn string, log *slog.Logger, handler EventHandler) error {
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return fmt.Errorf("listen connect: %w", err)
	}
	defer conn.Close(ctx)

	if _, err := conn.Exec(ctx, "LISTEN "+verdictChannel); err != nil {
		return fmt.Errorf("LISTEN %s: %w", verdictChannel, err)
	}
	log.Info("Listening for verdict events", "channel", verdictChannel)

	for {
		notification, err := conn.WaitForNotification(ctx)
		if err != nil {
			return err
		}
		var ev VerdictEvent
		if err := json.Unmarshal([]byte(notification.Payload), &ev); err != nil {
			log.Warn("Ignoring malformed verdict_events payload", "payload", notification.Payload, "err", err)
			continue
		}
		if handler != nil {
			handler(ev)
		}
	}
}
