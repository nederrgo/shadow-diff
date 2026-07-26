package parsers

import (
	"encoding/binary"
	"io"

	"go.mongodb.org/mongo-driver/bson"
)

const (
	mongoHeaderBytes = 16
	opMsg            = 2013
	sectionBody      = 0
	sectionDocSeq    = 1
)

// mongoParser decodes OP_MSG (opcode 2013) command documents.
//
// Only OP_MSG is decoded. Every server since MongoDB 3.6 negotiates it, and the
// legacy OP_QUERY opcodes carry only the connection handshake, which is untraced
// and therefore dropped downstream anyway.
type mongoParser struct{}

func (p *mongoParser) Run(r io.Reader, emit func(QueryReport)) error {
	header := make([]byte, mongoHeaderBytes)
	for {
		if _, err := io.ReadFull(r, header); err != nil {
			return err
		}
		msgLen := int(int32(binary.LittleEndian.Uint32(header[0:4])))
		opCode := int32(binary.LittleEndian.Uint32(header[12:16]))
		bodyLen := msgLen - mongoHeaderBytes
		if msgLen < mongoHeaderBytes || bodyLen < 0 {
			return ErrFrameTooLarge // unframeable; the stream cannot be trusted from here
		}
		// Oversized or uninteresting frames are drained, never buffered — the
		// stream stays framed for the next message without the allocation.
		if bodyLen > MaxFrameBytes || opCode != opMsg {
			if _, err := io.CopyN(io.Discard, r, int64(bodyLen)); err != nil {
				return err
			}
			continue
		}
		body := make([]byte, bodyLen)
		if _, err := io.ReadFull(r, body); err != nil {
			return err
		}
		if doc := opMsgBody(body); doc != nil {
			emit(mongoReport(doc))
		}
	}
}

// opMsgBody returns the kind-0 body section of an OP_MSG, or nil.
//
//	uint32 flagBits, then sections: [kind byte][payload]
//
// Kind-1 sections (document sequences, used to stream bulk insert documents) are
// skipped: the kind-0 body always names the command and collection, which is
// what the signature needs.
func opMsgBody(body []byte) bson.Raw {
	const flagBytes = 4
	for off := flagBytes; off < len(body); {
		kind := body[off]
		off++
		switch kind {
		case sectionBody:
			doc, n := readBSON(body[off:])
			if doc == nil {
				return nil
			}
			return doc[:n]
		case sectionDocSeq:
			if off+4 > len(body) {
				return nil
			}
			size := int(int32(binary.LittleEndian.Uint32(body[off : off+4])))
			if size < 4 || off+size > len(body) {
				return nil
			}
			off += size
		default:
			return nil
		}
	}
	return nil
}

func readBSON(b []byte) (bson.Raw, int) {
	if len(b) < 5 {
		return nil, 0
	}
	size := int(int32(binary.LittleEndian.Uint32(b[0:4])))
	if size < 5 || size > len(b) {
		return nil, 0
	}
	return bson.Raw(b), size
}

func mongoReport(doc bson.Raw) QueryReport {
	operation, target := mongoOpTarget(doc)
	// Relaxed extended JSON keeps the command document's top-level keys intact,
	// which is what lets Beru's mongoPayloadsEqual strip the per-connection noise
	// (_id, lsid, comment, $db) before diffing.
	raw, err := bson.MarshalExtJSON(doc, false, false)
	if err != nil {
		raw = nil
	}
	return QueryReport{
		TraceID:   TraceIDFrom(doc),
		Protocol:  ProtocolMongoDB,
		Operation: operation,
		Target:    target,
		RawQuery:  string(raw),
	}
}

// mongoOpTarget reads the command verb and collection. In the MongoDB command
// document the first element is the command name and its value is the collection
// — except for cursor-continuation commands like getMore, whose value is the
// cursor id and which name the collection in a separate field.
func mongoOpTarget(doc bson.Raw) (operation, target string) {
	elems, err := doc.Elements()
	if err != nil || len(elems) == 0 {
		return "", ""
	}
	operation = elems[0].Key()
	if col, ok := elems[0].Value().StringValueOK(); ok {
		return operation, col
	}
	if col, ok := doc.Lookup("collection").StringValueOK(); ok {
		return operation, col
	}
	return operation, ""
}
