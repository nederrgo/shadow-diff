package parsers

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/jackc/pgx/v5/pgproto3"
	"go.mongodb.org/mongo-driver/bson"
)

const testTraceparent = "00-4bf92f3577b34da6a3ce929d0e0e4736-0000000000000001-01"
const testTraceID = "4bf92f3577b34da6a3ce929d0e0e4736"

// collect drains a parser over the whole of in and returns everything it emitted.
func collect(t *testing.T, protocol string, in []byte) []QueryReport {
	t.Helper()
	p, err := New(protocol)
	if err != nil {
		t.Fatalf("New(%q) = %v", protocol, err)
	}
	var got []QueryReport
	// Run always ends in an error — EOF at best, a decode failure at worst. That
	// is the fail-open contract: the parser stops, the proxy keeps forwarding.
	// What matters is only what it emitted before stopping.
	_ = p.Run(bytes.NewReader(in), func(q QueryReport) { got = append(got, q) })
	return got
}

func TestTraceIDFrom(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"sql comment", "SELECT 1 /* traceparent='" + testTraceparent + "' */", testTraceID},
		{"bson comment field", `{"find":"users","$comment":"` + testTraceparent + `"}`, testTraceID},
		{"bare traceparent", testTraceparent, testTraceID},
		{"absent", "SELECT * FROM users", ""},
		{"uppercase hex is not a traceparent", "00-4BF92F3577B34DA6A3CE929D0E0E4736-0000000000000001-01", ""},
		{"truncated", "00-4bf92f35-0000000000000001-01", ""},
	}
	for _, tc := range tests {
		if got := TraceIDFrom([]byte(tc.in)); got != tc.want {
			t.Fatalf("%s: TraceIDFrom(%q) = %q want %q", tc.name, tc.in, got, tc.want)
		}
	}
}

func TestSQLOpTarget(t *testing.T) {
	t.Parallel()
	tests := []struct {
		sql     string
		wantOp  string
		wantTgt string
	}{
		{"SELECT * FROM users WHERE id = $1", "select", "users"},
		{"select id from public.users", "select", "users"},
		{`INSERT INTO "orders" (id) VALUES (1)`, "insert", "orders"},
		{"UPDATE users SET name = 'x'", "update", "users"},
		{"DELETE FROM sessions WHERE id = 1", "delete", "sessions"},
		{"SELECT * FROM [dbo].[Users]", "select", "users"},
		{"EXEC sp_do_thing", "exec", "sp_do_thing"},
		{"BEGIN", "begin", ""},
		{"", "", ""},
		// The traceparent comment must not be mistaken for a table name.
		{"/* traceparent='" + testTraceparent + "' */ SELECT * FROM users", "select", "users"},
		{"SELECT * FROM users -- FROM comments\n", "select", "users"},
	}
	for _, tc := range tests {
		op, tgt := sqlOpTarget(tc.sql)
		if op != tc.wantOp || tgt != tc.wantTgt {
			t.Fatalf("sqlOpTarget(%q) = (%q, %q) want (%q, %q)", tc.sql, op, tgt, tc.wantOp, tc.wantTgt)
		}
	}
}

func TestSignature(t *testing.T) {
	t.Parallel()
	tests := []struct {
		q    QueryReport
		want string
	}{
		{QueryReport{Protocol: ProtocolPostgres, Operation: "select", Target: "users"}, "postgresql:select:users"},
		// MongoDB command names keep their canonical camelCase.
		{QueryReport{Protocol: ProtocolMongoDB, Operation: "findAndModify", Target: "orders"}, "mongodb:findAndModify:orders"},
		{QueryReport{Protocol: ProtocolMongoDB, Operation: "insert", Target: "orders"}, "mongodb:insert:orders"},
		{QueryReport{Protocol: ProtocolRedis, Operation: "get", Target: "session:1"}, "redis:get:session:1"},
		{QueryReport{Protocol: ProtocolMSSQL, Operation: "", Target: ""}, "mssql:unknown:-"},
	}
	for _, tc := range tests {
		if got := tc.q.Signature(); got != tc.want {
			t.Fatalf("Signature(%+v) = %q want %q", tc.q, got, tc.want)
		}
	}
}

// --- Postgres ---

// pgStream builds a frontend byte stream: StartupMessage followed by msgs.
func pgStream(t *testing.T, msgs ...pgproto3.FrontendMessage) []byte {
	t.Helper()
	startup := &pgproto3.StartupMessage{ProtocolVersion: pgproto3.ProtocolVersionNumber, Parameters: map[string]string{"user": "u"}}
	buf, err := startup.Encode(nil)
	if err != nil {
		t.Fatalf("encode startup: %v", err)
	}
	for _, m := range msgs {
		buf, err = m.Encode(buf)
		if err != nil {
			t.Fatalf("encode %T: %v", m, err)
		}
	}
	return buf
}

func TestPostgresSimpleQuery(t *testing.T) {
	t.Parallel()
	sql := "/* traceparent='" + testTraceparent + "' */ SELECT * FROM users WHERE id = 1"
	got := collect(t, ProtocolPostgres, pgStream(t, &pgproto3.Query{String: sql}))
	if len(got) != 1 {
		t.Fatalf("got %d reports, want 1: %+v", len(got), got)
	}
	if got[0].Signature() != "postgresql:select:users" {
		t.Fatalf("Signature = %q want postgresql:select:users", got[0].Signature())
	}
	if got[0].TraceID != testTraceID {
		t.Fatalf("TraceID = %q want %q", got[0].TraceID, testTraceID)
	}
	if got[0].RawQuery != sql {
		t.Fatalf("RawQuery = %q want %q", got[0].RawQuery, sql)
	}
}

// Extended protocol carries the SQL on Parse and the values on Bind, so a report
// must only be emitted once both have arrived — otherwise every pgx query (which
// always uses the extended protocol) reports with no parameters.
func TestPostgresExtendedQueryPairsParseWithBind(t *testing.T) {
	t.Parallel()
	sql := "SELECT * FROM users WHERE id = $1 /* traceparent='" + testTraceparent + "' */"
	stream := pgStream(t,
		&pgproto3.Parse{Name: "s1", Query: sql},
		&pgproto3.Bind{PreparedStatement: "s1", Parameters: [][]byte{[]byte("123")}},
	)
	got := collect(t, ProtocolPostgres, stream)
	if len(got) != 1 {
		t.Fatalf("got %d reports, want 1: %+v", len(got), got)
	}
	if got[0].Signature() != "postgresql:select:users" {
		t.Fatalf("Signature = %q", got[0].Signature())
	}
	if len(got[0].Params) != 1 || got[0].Params[0] != "123" {
		t.Fatalf("Params = %v want [123]", got[0].Params)
	}
	if got[0].TraceID != testTraceID {
		t.Fatalf("TraceID = %q", got[0].TraceID)
	}
}

// A Parse alone is not a query — reporting it would double-count every extended
// protocol statement against control-a and trip MISMATCH_COUNT.
func TestPostgresParseWithoutBindEmitsNothing(t *testing.T) {
	t.Parallel()
	got := collect(t, ProtocolPostgres, pgStream(t, &pgproto3.Parse{Name: "s1", Query: "SELECT 1"}))
	if len(got) != 0 {
		t.Fatalf("got %d reports, want 0: %+v", len(got), got)
	}
}

func TestPostgresBindForUnknownStatementIsSkipped(t *testing.T) {
	t.Parallel()
	got := collect(t, ProtocolPostgres, pgStream(t, &pgproto3.Bind{PreparedStatement: "never-parsed"}))
	if len(got) != 0 {
		t.Fatalf("got %d reports, want 0: %+v", len(got), got)
	}
}

func TestRenderParams(t *testing.T) {
	t.Parallel()
	got := renderParams([][]byte{[]byte("abc"), nil, {0xff, 0xfe}})
	want := []string{"abc", "NULL", "0xfffe"}
	if len(got) != len(want) {
		t.Fatalf("renderParams len = %d want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("renderParams[%d] = %q want %q", i, got[i], want[i])
		}
	}
}

// --- MongoDB ---

func opMsgFrame(t *testing.T, doc bson.D) []byte {
	t.Helper()
	body, err := bson.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	payload := make([]byte, 4, 4+1+len(body))
	payload = append(payload, sectionBody)
	payload = append(payload, body...)

	frame := make([]byte, mongoHeaderBytes+len(payload))
	binary.LittleEndian.PutUint32(frame[0:4], uint32(mongoHeaderBytes+len(payload)))
	binary.LittleEndian.PutUint32(frame[12:16], uint32(opMsg))
	copy(frame[mongoHeaderBytes:], payload)
	return frame
}

func TestMongoOpMsg(t *testing.T) {
	t.Parallel()
	doc := bson.D{
		{Key: "insert", Value: "orders"},
		{Key: "$db", Value: "shop"},
		{Key: "comment", Value: testTraceparent},
	}
	got := collect(t, ProtocolMongoDB, opMsgFrame(t, doc))
	if len(got) != 1 {
		t.Fatalf("got %d reports, want 1: %+v", len(got), got)
	}
	if got[0].Signature() != "mongodb:insert:orders" {
		t.Fatalf("Signature = %q want mongodb:insert:orders", got[0].Signature())
	}
	if got[0].TraceID != testTraceID {
		t.Fatalf("TraceID = %q want %q", got[0].TraceID, testTraceID)
	}
	// Extended JSON must keep $db/comment at the top level so Beru can strip
	// them before diffing.
	if !strings.Contains(got[0].RawQuery, `"$db"`) || !strings.Contains(got[0].RawQuery, `"insert":"orders"`) {
		t.Fatalf("RawQuery = %q", got[0].RawQuery)
	}
}

// getMore names the collection in a separate field because its command value is
// the cursor id, not a string.
func TestMongoGetMoreUsesCollectionField(t *testing.T) {
	t.Parallel()
	doc := bson.D{
		{Key: "getMore", Value: int64(42)},
		{Key: "collection", Value: "orders"},
	}
	got := collect(t, ProtocolMongoDB, opMsgFrame(t, doc))
	if len(got) != 1 || got[0].Signature() != "mongodb:getMore:orders" {
		t.Fatalf("got %+v", got)
	}
}

// A non-OP_MSG frame must be drained, not misparsed, and must not desync the
// frames that follow it.
func TestMongoSkipsLegacyOpcodeAndStaysFramed(t *testing.T) {
	t.Parallel()
	legacy := make([]byte, mongoHeaderBytes+8)
	binary.LittleEndian.PutUint32(legacy[0:4], uint32(len(legacy)))
	binary.LittleEndian.PutUint32(legacy[12:16], 2004) // OP_QUERY

	stream := append(legacy, opMsgFrame(t, bson.D{{Key: "find", Value: "users"}})...)
	got := collect(t, ProtocolMongoDB, stream)
	if len(got) != 1 || got[0].Signature() != "mongodb:find:users" {
		t.Fatalf("got %+v", got)
	}
}

func TestMongoTruncatedFrameDoesNotPanic(t *testing.T) {
	t.Parallel()
	full := opMsgFrame(t, bson.D{{Key: "find", Value: "users"}})
	if got := collect(t, ProtocolMongoDB, full[:len(full)-4]); len(got) != 0 {
		t.Fatalf("got %+v, want none", got)
	}
}

func TestMongoOversizedFrameIsSkipped(t *testing.T) {
	t.Parallel()
	frame := make([]byte, mongoHeaderBytes)
	binary.LittleEndian.PutUint32(frame[0:4], uint32(mongoHeaderBytes+MaxFrameBytes+1))
	binary.LittleEndian.PutUint32(frame[12:16], uint32(opMsg))
	// Only the header is present; draining the declared body hits EOF, which is
	// the expected non-panicking outcome.
	if got := collect(t, ProtocolMongoDB, frame); len(got) != 0 {
		t.Fatalf("got %+v, want none", got)
	}
}

// --- Redis ---

func TestRedisRESPArray(t *testing.T) {
	t.Parallel()
	in := "*3\r\n$3\r\nSET\r\n$9\r\nsession:1\r\n$5\r\nvalue\r\n"
	got := collect(t, ProtocolRedis, []byte(in))
	if len(got) != 1 {
		t.Fatalf("got %d reports, want 1: %+v", len(got), got)
	}
	if got[0].Signature() != "redis:set:session:1" {
		t.Fatalf("Signature = %q", got[0].Signature())
	}
	if len(got[0].Params) != 2 {
		t.Fatalf("Params = %v", got[0].Params)
	}
}

func TestRedisInlineCommand(t *testing.T) {
	t.Parallel()
	got := collect(t, ProtocolRedis, []byte("PING\r\n"))
	if len(got) != 1 || got[0].Signature() != "redis:ping:-" {
		t.Fatalf("got %+v", got)
	}
}

func TestRedisMultipleCommandsStayFramed(t *testing.T) {
	t.Parallel()
	in := "*2\r\n$3\r\nGET\r\n$1\r\na\r\n*2\r\n$3\r\nGET\r\n$1\r\nb\r\n"
	got := collect(t, ProtocolRedis, []byte(in))
	if len(got) != 2 {
		t.Fatalf("got %d reports, want 2: %+v", len(got), got)
	}
	if got[0].Target != "a" || got[1].Target != "b" {
		t.Fatalf("targets = %q, %q", got[0].Target, got[1].Target)
	}
}

func TestRedisTraceparentInArgument(t *testing.T) {
	t.Parallel()
	in := "*3\r\n$3\r\nSET\r\n$1\r\nk\r\n$55\r\n" + testTraceparent + "\r\n"
	got := collect(t, ProtocolRedis, []byte(in))
	if len(got) != 1 || got[0].TraceID != testTraceID {
		t.Fatalf("got %+v", got)
	}
}

func TestRedisMalformedDoesNotPanic(t *testing.T) {
	t.Parallel()
	for _, in := range []string{"*2\r\n+OK\r\n", "*abc\r\n", "*1\r\n$99\r\nshort", "*1\r\n"} {
		_ = collect(t, ProtocolRedis, []byte(in))
	}
}

// --- MSSQL ---

func tdsPacket(msgType byte, payload []byte) []byte {
	pkt := make([]byte, tdsHeaderBytes+len(payload))
	pkt[0] = msgType
	pkt[1] = tdsStatusEOM
	binary.BigEndian.PutUint16(pkt[2:4], uint16(tdsHeaderBytes+len(payload)))
	copy(pkt[tdsHeaderBytes:], payload)
	return pkt
}

func ucs2(s string) []byte {
	u := utf16.Encode([]rune(s))
	b := make([]byte, len(u)*2)
	for i, c := range u {
		binary.LittleEndian.PutUint16(b[i*2:], c)
	}
	return b
}

func allHeaders() []byte {
	h := make([]byte, 22)
	binary.LittleEndian.PutUint32(h[0:4], uint32(len(h)))
	return h
}

func TestMSSQLSQLBatch(t *testing.T) {
	t.Parallel()
	sql := "SELECT * FROM Users /* traceparent='" + testTraceparent + "' */"
	payload := append(allHeaders(), ucs2(sql)...)
	got := collect(t, ProtocolMSSQL, tdsPacket(tdsSQLBatch, payload))
	if len(got) != 1 {
		t.Fatalf("got %d reports, want 1: %+v", len(got), got)
	}
	if got[0].Signature() != "mssql:select:users" {
		t.Fatalf("Signature = %q", got[0].Signature())
	}
	if got[0].TraceID != testTraceID {
		t.Fatalf("TraceID = %q", got[0].TraceID)
	}
}

// A batch with no ALL_HEADERS block must still decode: the length probe has to
// tell UCS-2 text apart from a header block.
func TestMSSQLSQLBatchWithoutALLHeaders(t *testing.T) {
	t.Parallel()
	got := collect(t, ProtocolMSSQL, tdsPacket(tdsSQLBatch, ucs2("SELECT * FROM Orders")))
	if len(got) != 1 || got[0].Signature() != "mssql:select:orders" {
		t.Fatalf("got %+v", got)
	}
}

// A request split across packets is one logical statement; only the last packet
// carries EOM.
func TestMSSQLMultiPacketMessage(t *testing.T) {
	t.Parallel()
	sql := ucs2("SELECT * FROM Users")
	payload := append(allHeaders(), sql...)
	split := len(payload) / 2
	if split%2 != 0 {
		split++ // never split a UCS-2 code unit
	}

	first := tdsPacket(tdsSQLBatch, payload[:split])
	first[1] = 0 // not EOM
	stream := append(first, tdsPacket(tdsSQLBatch, payload[split:])...)

	got := collect(t, ProtocolMSSQL, stream)
	if len(got) != 1 || got[0].Signature() != "mssql:select:users" {
		t.Fatalf("got %+v", got)
	}
}

func TestMSSQLRPCExecuteSQL(t *testing.T) {
	t.Parallel()
	sql := "SELECT * FROM Orders WHERE id = @p1"
	payload := allHeaders()
	payload = binary.LittleEndian.AppendUint16(payload, uint16(len("sp_executesql")))
	payload = append(payload, ucs2("sp_executesql")...)
	payload = append(payload, 0x00, 0x00) // flags
	payload = append(payload, ucs2(sql)...)

	got := collect(t, ProtocolMSSQL, tdsPacket(tdsRPCRequest, payload))
	if len(got) != 1 || got[0].Signature() != "mssql:select:orders" {
		t.Fatalf("got %+v", got)
	}
}

func TestMSSQLTruncatedDoesNotPanic(t *testing.T) {
	t.Parallel()
	pkt := tdsPacket(tdsSQLBatch, append(allHeaders(), ucs2("SELECT 1")...))
	if got := collect(t, ProtocolMSSQL, pkt[:len(pkt)-6]); len(got) != 0 {
		t.Fatalf("got %+v, want none", got)
	}
}

func TestNewUnsupportedProtocol(t *testing.T) {
	t.Parallel()
	if _, err := New("cassandra"); err == nil {
		t.Fatal("New(cassandra) = nil error, want error")
	}
	if !Supported(ProtocolMongoDB) || Supported("cassandra") {
		t.Fatal("Supported disagrees with New")
	}
}
