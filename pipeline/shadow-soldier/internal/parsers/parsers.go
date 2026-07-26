// Package parsers decodes database wire protocols into QueryReports.
//
// Every parser reads the client->server half of a proxied connection and is
// deliberately best-effort: a frame it cannot decode is skipped, never fatal.
// The proxy stays fail-open, so a parser bug must never break the application's
// database socket.
package parsers

import (
	"errors"
	"fmt"
	"io"
	"strings"
)

// MaxFrameBytes caps a single parsed command. Frames larger than this are
// skipped rather than allocated, so a large or hostile payload cannot grow the
// sidecar's heap past its memory limit.
const MaxFrameBytes = 64 * 1024

// Supported wire protocols. These strings are sent to Beru verbatim as the
// report's `protocol` field, which buckets the diff — they must stay lowercase
// and must not change once reports exist in a database.
const (
	ProtocolPostgres = "postgresql"
	ProtocolMongoDB  = "mongodb"
	ProtocolRedis    = "redis"
	ProtocolMSSQL    = "mssql"
)

// ErrFrameTooLarge is returned when a frame declares a length over MaxFrameBytes.
var ErrFrameTooLarge = errors.New("frame exceeds max size")

// Parser decodes the client->server half of one proxied connection.
type Parser interface {
	// Run consumes r until it is exhausted or a frame cannot be decoded,
	// calling emit for each command it recognises. A non-nil return ends
	// parsing for that connection; the proxy keeps forwarding bytes regardless.
	Run(r io.Reader, emit func(QueryReport)) error
}

// New returns a fresh Parser for protocol. Parsers are stateful per connection
// (prepared statements, stream framing), so callers must not share one.
func New(protocol string) (Parser, error) {
	switch protocol {
	case ProtocolPostgres:
		return newPostgresParser(), nil
	case ProtocolMongoDB:
		return &mongoParser{}, nil
	case ProtocolRedis:
		return &redisParser{}, nil
	case ProtocolMSSQL:
		return &mssqlParser{}, nil
	default:
		return nil, fmt.Errorf("unsupported protocol %q", protocol)
	}
}

// Supported reports whether protocol has a parser. Monarch uses the same set to
// decide which dependencies get proxied.
func Supported(protocol string) bool {
	_, err := New(protocol)
	return err == nil
}

// QueryReport is one decoded database command.
type QueryReport struct {
	TraceID   string   // bare 32-hex W3C trace id, "" when the command carried none
	Protocol  string   // mongodb | redis | postgresql | mssql
	Operation string   // find | insert | select | get | execute ...
	Target    string   // collection, table, or key
	RawQuery  string   // raw SQL, or extended-JSON command document
	Params    []string // bind parameters, when the protocol exposes them
}

// Signature is the correlation key Beru buckets egress reports by. It must be
// identical across control-a, control-b and candidate for the same logical
// operation, so it deliberately excludes anything value-dependent.
// Each parser normalizes its own verb casing on the way in — SQL verbs are
// lower-cased, Redis commands are lower-cased, and MongoDB command names keep the
// canonical camelCase the driver puts on the wire (findAndModify, getMore), which
// is also how Beru's own MongoSignature spells them.
func (q QueryReport) Signature() string {
	op := strings.TrimSpace(q.Operation)
	target := strings.TrimSpace(q.Target)
	if op == "" {
		op = "unknown"
	}
	if target == "" {
		target = "-"
	}
	return fmt.Sprintf("%s:%s:%s", q.Protocol, op, target)
}
