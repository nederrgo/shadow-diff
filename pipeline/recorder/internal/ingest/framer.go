package ingest

import (
	"encoding/binary"
	"io"
	"log"
	"net"
	"sync"
)

// HandleConn reads framed bytes from Siphon until EOF or error.
func HandleConn(conn net.Conn, store *SessionStore, connID uint64) {
	defer func() { _ = conn.Close() }()

	header := make([]byte, FrameHeaderSize)
	var loggedFirst sync.Once
	for {
		_, err := io.ReadFull(conn, header)
		if err != nil {
			if err != io.EOF && err != io.ErrUnexpectedEOF {
				log.Printf("recorder: conn=%d read header: %v", connID, err)
			}
			store.FinishConn(connID)
			return
		}

		dir := header[0]
		if dir != DirRequest && dir != DirResponse {
			log.Printf("recorder: conn=%d invalid direction %q", connID, dir)
			store.DiscardConn(connID)
			return
		}

		length := binary.BigEndian.Uint32(header[1:5])
		if length == 0 {
			continue
		}
		if int(length) > store.maxFrame {
			log.Printf("recorder: conn=%d frame length %d exceeds max %d", connID, length, store.maxFrame)
			store.DiscardConn(connID)
			return
		}

		payload := make([]byte, length)
		if _, err := io.ReadFull(conn, payload); err != nil {
			if err != io.EOF && err != io.ErrUnexpectedEOF {
				log.Printf("recorder: conn=%d read payload: %v", connID, err)
			}
			store.DiscardConn(connID)
			return
		}

		if err := store.WriteFrame(connID, dir, payload); err != nil {
			log.Printf("recorder: conn=%d write frame: %v", connID, err)
			store.DiscardConn(connID)
			return
		}
		loggedFirst.Do(func() {
			dirName := "response"
			if dir == DirRequest {
				dirName = "request"
			}
			log.Printf("recorder debug: conn=%d first frame dir=%s len=%d", connID, dirName, len(payload))
		})
	}
}
