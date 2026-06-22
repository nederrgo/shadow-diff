package egress

import (
	"context"
	"encoding/binary"
	"log"
	"net"
	"sync"
	"time"

	"github.com/shadow-diff/siphon/internal/forward"
)

const (
	dirRequest      = 'R'
	dirResponse     = 'S'
	frameHeaderSize = 5
	maxFrameBytes   = 5 << 20
)

// runRelay dials Recorder and sends both legs as length-prefixed frames.
func runRelay(sess *Session, poolMgr *forward.PoolManager) {
	defer sess.clearRelayRunning()

	target := sess.Target
	if target == nil || target.RecorderHost == "" {
		log.Printf("egress relay: recorder_host not configured for flow %s", sess.flowKey)
		return
	}

	reqPayload, resPayload := sess.snapshotPayloads()
	if len(reqPayload) == 0 && len(resPayload) == 0 {
		log.Printf("egress relay: empty payloads for flow %s", sess.flowKey)
		return
	}

	dest := target.RecorderHost
	pool := poolMgr.GetPool(dest)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	conn, err := pool.Dial(ctx)
	if err != nil {
		log.Printf("egress relay: dial %s failed: %v", dest, err)
		return
	}
	defer func() { _ = conn.Close() }()
	log.Printf("siphon debug: egress relay connected flow=%s dest=%s req=%d res=%d bytes",
		sess.flowKey, dest, len(reqPayload), len(resPayload))

	var writeMu sync.Mutex
	sendFrame(&writeMu, conn, dirRequest, reqPayload, sess.flowKey)
	sendFrame(&writeMu, conn, dirResponse, resPayload, sess.flowKey)
	sess.clearPayloads()
	sess.resetForKeepAlive()
}

func sendFrame(writeMu *sync.Mutex, conn net.Conn, dir byte, payload []byte, flowKey string) {
	if len(payload) == 0 {
		return
	}
	dirName := "response"
	if dir == dirRequest {
		dirName = "request"
	}
	log.Printf("siphon debug: egress relay frame flow=%s dir=%s nbytes=%d preview=%q",
		flowKey, dirName, len(payload), relayPayloadPreview(payload, 160))
	writeMu.Lock()
	werr := writeFrame(conn, dir, payload)
	writeMu.Unlock()
	if werr != nil {
		log.Printf("egress relay: write frame flow=%s dir=%s: %v", flowKey, dirName, werr)
	}
}

func writeFrame(conn net.Conn, dir byte, payload []byte) error {
	if len(payload) == 0 {
		return nil
	}
	if len(payload) > maxFrameBytes {
		log.Printf("egress relay: frame exceeds max size, truncating")
		payload = payload[:maxFrameBytes]
	}
	hdr := make([]byte, frameHeaderSize)
	hdr[0] = dir
	binary.BigEndian.PutUint32(hdr[1:5], uint32(len(payload)))
	if _, err := conn.Write(hdr); err != nil {
		return err
	}
	_, err := conn.Write(payload)
	return err
}
