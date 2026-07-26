package parsers

import (
	"encoding/hex"
	"fmt"
	"io"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgproto3"
)

// maxPreparedStatements bounds the Parse->Bind correlation map. A connection
// that prepares more than this many distinct statements loses the SQL text for
// the oldest ones; it still reports, with an empty RawQuery.
const maxPreparedStatements = 256

// postgresParser decodes the frontend half of a PostgreSQL connection.
//
// It handles both query protocols:
//
//	simple   — Query('Q') carries the SQL and no parameters; emitted immediately.
//	extended — Parse('P') carries the SQL, Bind('B') carries the parameter values.
//	           The report is held until Bind so the two can be reported together,
//	           which is what makes the payload's "parameters" field populated for
//	           every driver that uses prepared statements (pgx does, by default).
type postgresParser struct {
	prepared map[string]string // statement name -> SQL
}

func newPostgresParser() *postgresParser {
	return &postgresParser{prepared: make(map[string]string)}
}

func (p *postgresParser) Run(r io.Reader, emit func(QueryReport)) error {
	// The SSLRequest, if any, was answered and consumed by the proxy before the
	// tap was opened, so this stream always begins at the StartupMessage.
	backend := pgproto3.NewBackend(r, io.Discard)
	if _, err := backend.ReceiveStartupMessage(); err != nil {
		return err
	}
	for {
		msg, err := backend.Receive()
		if err != nil {
			return err
		}
		switch m := msg.(type) {
		case *pgproto3.Query:
			emit(p.report(m.String, nil))
		case *pgproto3.Parse:
			if len(p.prepared) >= maxPreparedStatements {
				clear(p.prepared)
			}
			p.prepared[m.Name] = m.Query
		case *pgproto3.Bind:
			sql, ok := p.prepared[m.PreparedStatement]
			if !ok {
				// Statement prepared before the proxy saw this connection.
				continue
			}
			emit(p.report(sql, m.Parameters))
		}
	}
}

func (p *postgresParser) report(sql string, params [][]byte) QueryReport {
	op, target := sqlOpTarget(sql)
	return QueryReport{
		TraceID:   TraceIDFrom([]byte(sql)),
		Protocol:  ProtocolPostgres,
		Operation: op,
		Target:    target,
		RawQuery:  sql,
		Params:    renderParams(params),
	}
}

// renderParams turns wire parameter bytes into diffable strings. Postgres sends
// parameters in either text or binary format and the format codes live on the
// same Bind message, but decoding binary values needs the type OIDs from Parse
// — which the client is free to leave unspecified. Rendering non-UTF-8 as hex is
// both stable across roles and honest about what was on the wire.
func renderParams(params [][]byte) []string {
	if len(params) == 0 {
		return nil
	}
	out := make([]string, 0, len(params))
	for _, v := range params {
		switch {
		case v == nil:
			out = append(out, "NULL")
		case utf8.Valid(v):
			out = append(out, string(v))
		default:
			out = append(out, fmt.Sprintf("0x%s", hex.EncodeToString(v)))
		}
	}
	return out
}
