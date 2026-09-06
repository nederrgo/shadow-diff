package replay

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/shadow-diff/igris/internal/driver"
	"github.com/shadow-diff/igris/internal/payload"
	pkgreplay "github.com/shadow-diff/replay"
)

// Engine holds preloaded ingress records and multicasts them on Start.
type Engine struct {
	inner *pkgreplay.Engine[driver.IngressCapture]
}

// NewEngine builds a replay engine over HTTP captures.
func NewEngine(records []driver.IngressCapture, targets []payload.Target, client *http.Client, log *slog.Logger) *Engine {
	if client == nil {
		client = &http.Client{}
	}
	if log == nil {
		log = slog.Default()
	}
	return &Engine{inner: &pkgreplay.Engine[driver.IngressCapture]{
		Records: records,
		Log:     log,
		Dispatch: func(_ context.Context, rec driver.IngressCapture) {
			dispatchRecord(client, log, rec, targets)
		},
	}}
}

func (e *Engine) Start() (total int, already bool) { return e.inner.Start() }
func (e *Engine) Wait()                            { e.inner.Wait() }
func (e *Engine) Running() bool                    { return e.inner.Running() }
