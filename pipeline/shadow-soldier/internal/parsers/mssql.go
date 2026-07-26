package parsers

import (
	"encoding/binary"
	"io"
	"strings"
	"unicode/utf16"
)

const (
	tdsHeaderBytes = 8
	tdsSQLBatch    = 0x01
	tdsRPCRequest  = 0x03
	tdsStatusEOM   = 0x01

	// maxALLHeaders bounds the ALL_HEADERS block a TDS 7.2+ request prefixes to
	// its payload. Real blocks are tens of bytes; the bound is what lets a plain
	// length read distinguish "this is ALL_HEADERS" from "this is UCS-2 text",
	// whose first four bytes always decode to an implausibly large number.
	maxALLHeaders = 1024
)

// mssqlParser decodes TDS SQLBatch and RPCRequest packets.
//
// go-mssqldb keeps its TDS framing unexported in its root package, so there is
// no protocol API to import; the framing below is the wire format itself.
type mssqlParser struct{}

func (p *mssqlParser) Run(r io.Reader, emit func(QueryReport)) error {
	header := make([]byte, tdsHeaderBytes)
	for {
		msgType, payload, err := readTDSMessage(r, header)
		if err != nil {
			return err
		}
		var sql string
		switch msgType {
		case tdsSQLBatch:
			sql = decodeUCS2(skipALLHeaders(payload))
		case tdsRPCRequest:
			sql = rpcSQL(payload)
		default:
			continue
		}
		if strings.TrimSpace(sql) == "" {
			continue
		}
		op, target := sqlOpTarget(sql)
		emit(QueryReport{
			TraceID:   TraceIDFrom([]byte(sql)),
			Protocol:  ProtocolMSSQL,
			Operation: op,
			Target:    target,
			RawQuery:  sql,
		})
	}
}

// readTDSMessage reassembles one logical TDS message. A single request may span
// several packets; only the last carries the EOM status bit.
func readTDSMessage(r io.Reader, header []byte) (msgType byte, payload []byte, err error) {
	var buf []byte
	for {
		if _, err := io.ReadFull(r, header); err != nil {
			return 0, nil, err
		}
		msgType = header[0]
		status := header[1]
		length := int(binary.BigEndian.Uint16(header[2:4]))
		if length < tdsHeaderBytes {
			return 0, nil, ErrFrameTooLarge
		}
		chunk := length - tdsHeaderBytes

		if len(buf)+chunk > MaxFrameBytes {
			// Drain the rest of this message so the stream stays framed, then
			// report it as skipped rather than buffering an unbounded batch.
			if _, err := io.CopyN(io.Discard, r, int64(chunk)); err != nil {
				return 0, nil, err
			}
			if status&tdsStatusEOM != 0 {
				return 0, nil, ErrFrameTooLarge
			}
			continue
		}

		part := make([]byte, chunk)
		if _, err := io.ReadFull(r, part); err != nil {
			return 0, nil, err
		}
		buf = append(buf, part...)
		if status&tdsStatusEOM != 0 {
			return msgType, buf, nil
		}
	}
}

// skipALLHeaders strips the optional ALL_HEADERS block that TDS 7.2+ prefixes to
// SQLBatch and RPCRequest payloads.
func skipALLHeaders(payload []byte) []byte {
	if len(payload) < 4 {
		return payload
	}
	total := int(binary.LittleEndian.Uint32(payload[0:4]))
	if total >= 4 && total <= maxALLHeaders && total <= len(payload) {
		return payload[total:]
	}
	return payload
}

// rpcSQL extracts the statement from an RPCRequest.
//
// ponytail: only sp_executesql is decoded, by taking the first NVARCHAR
// parameter — that is the call every parameterized ADO.NET and JDBC query
// compiles to. Ceiling: an RPC to a user stored procedure reports the procedure
// name with no statement text, and other system procedures are not decoded.
// Upgrade path is a full TDS parameter walk (type token, collation, length) if
// stored-procedure bodies ever need diffing.
func rpcSQL(payload []byte) string {
	body := skipALLHeaders(payload)
	if len(body) < 2 {
		return ""
	}
	nameLen := int(binary.LittleEndian.Uint16(body[0:2]))
	if nameLen == 0xFFFF {
		// ProcID form: uint16 0xFFFF followed by the numeric procedure id.
		// sp_executesql is procedure 10.
		if len(body) < 4 || binary.LittleEndian.Uint16(body[2:4]) != 10 {
			return ""
		}
		return longestUCS2Run(body[4:])
	}
	nameBytes := nameLen * 2
	if nameBytes < 0 || 2+nameBytes > len(body) {
		return ""
	}
	name := decodeUCS2(body[2 : 2+nameBytes])
	if !strings.EqualFold(name, "sp_executesql") {
		return name
	}
	return longestUCS2Run(body[2+nameBytes:])
}

// longestUCS2Run returns the longest plausible UCS-2 text span in b. In an
// sp_executesql call the parameter list holds the statement, its parameter
// declaration, and the values; the statement is reliably the longest of them.
func longestUCS2Run(b []byte) string {
	var best string
	var cur []uint16
	flush := func() {
		if len(cur) > 0 {
			if s := string(utf16.Decode(cur)); len(s) > len(best) {
				best = s
			}
			cur = cur[:0]
		}
	}
	for i := 0; i+1 < len(b); i += 2 {
		lo, hi := b[i], b[i+1]
		// Printable BMP characters only: anything else ends the run.
		if hi == 0 && (lo == '\t' || lo == '\n' || lo == '\r' || (lo >= 0x20 && lo < 0x7F)) {
			cur = append(cur, uint16(lo))
			continue
		}
		flush()
	}
	flush()
	if len(best) < 4 {
		return ""
	}
	return best
}

func decodeUCS2(b []byte) string {
	u := make([]uint16, 0, len(b)/2)
	for i := 0; i+1 < len(b); i += 2 {
		u = append(u, binary.LittleEndian.Uint16(b[i:i+2]))
	}
	return string(utf16.Decode(u))
}
