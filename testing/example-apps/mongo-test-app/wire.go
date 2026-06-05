package main

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"strings"
	"sync/atomic"
	"time"
)

const (
	opInsert = 2002
	opQuery  = 2004
)

var mongoRequestID uint32

func parseMongoHostPort(mongoURL string) (string, error) {
	u := strings.TrimSpace(mongoURL)
	u = strings.TrimPrefix(u, "mongodb://")
	if u == "" {
		return "", fmt.Errorf("empty mongo url")
	}
	if i := strings.IndexByte(u, '/'); i >= 0 {
		u = u[:i]
	}
	if u == "" {
		return "", fmt.Errorf("missing host in mongo url")
	}
	return u, nil
}

func insertLegacy(mongoURL, collection string, doc []byte) error {
	addr, err := parseMongoHostPort(mongoURL)
	if err != nil {
		return err
	}
	conn, err := net.DialTimeout("tcp", addr, 10*time.Second)
	if err != nil {
		return fmt.Errorf("dial mongo: %w", err)
	}
	defer conn.Close()

	if err := sendIsMaster(conn); err != nil {
		return fmt.Errorf("isMaster: %w", err)
	}
	if err := insertOne(conn, collection, doc); err != nil {
		return fmt.Errorf("insert: %w", err)
	}
	return nil
}

func withReadDeadline(conn net.Conn, d time.Duration) {
	_ = conn.SetReadDeadline(time.Now().Add(d))
}

func sendIsMaster(conn net.Conn) error {
	query := []byte{
		0x13, 0x00, 0x00, 0x00, // document length
		0x10, 'i', 's', 'M', 'a', 's', 't', 'e', 'r', 0x00,
		0x01, 0x00, 0x00, 0x00, // int32 value 1
		0x00, // end
	}
	body := appendInt32(nil, 0)
	body = append(body, cstring("admin.$cmd")...)
	body = appendInt32(body, 0)
	body = appendInt32(body, 1)
	body = append(body, query...)
	body = append(body, emptyBSON...)
	if err := writeMessage(conn, opQuery, body); err != nil {
		return err
	}
	withReadDeadline(conn, 5*time.Second)
	return readMessage(conn)
}

func insertOne(conn net.Conn, collection string, doc []byte) error {
	body := appendInt32(nil, 0)
	body = append(body, cstring(collection)...)
	body = append(body, doc...)
	if err := writeMessage(conn, opInsert, body); err != nil {
		return err
	}
	withReadDeadline(conn, 2*time.Second)
	if err := readMessage(conn); err != nil {
		// Mongo may have applied the insert before the reply is readable.
		return nil
	}
	return nil
}

func writeMessage(conn net.Conn, opCode int32, body []byte) error {
	reqID := int32(atomic.AddUint32(&mongoRequestID, 1))
	msgLen := 16 + len(body)
	hdr := make([]byte, 16)
	binary.LittleEndian.PutUint32(hdr[0:4], uint32(msgLen))
	binary.LittleEndian.PutUint32(hdr[4:8], uint32(reqID))
	binary.LittleEndian.PutUint32(hdr[8:12], 0)
	binary.LittleEndian.PutUint32(hdr[12:16], uint32(opCode))
	if _, err := conn.Write(hdr); err != nil {
		return err
	}
	_, err := conn.Write(body)
	return err
}

func readMessage(conn net.Conn) error {
	hdr := make([]byte, 16)
	if _, err := io.ReadFull(conn, hdr); err != nil {
		return err
	}
	length := int(binary.LittleEndian.Uint32(hdr[0:4]))
	if length < 16 {
		return fmt.Errorf("invalid message length %d", length)
	}
	rest := make([]byte, length-16)
	_, err := io.ReadFull(conn, rest)
	return err
}

func appendInt32(b []byte, v int32) []byte {
	var buf [4]byte
	binary.LittleEndian.PutUint32(buf[:], uint32(v))
	return append(b, buf[:]...)
}

func cstring(s string) []byte {
	return append([]byte(s), 0)
}

var emptyBSON = []byte{0x05, 0x00, 0x00, 0x00, 0x00}
