package egress

import (
	"context"
	"encoding/binary"
	"io"
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

// runRelay dials Recorder and copies both pipe legs as length-prefixed frames.
// Both reqR and resR are allocated at session init; streamFrames block on the
// pipe readers until the assembler delivers bytes.
func runRelay(sess *Session, poolMgr *forward.PoolManager) {
	defer sess.clearRelayRunning()

	target := sess.Target
	if target == nil || target.RecorderHost == "" {
		log.Printf("egress relay: recorder_host not configured for flow %s", sess.flowKey)
		return
	}

	reqR := sess.reqR
	resR := sess.resR
	if reqR == nil || resR == nil {
		log.Printf("egress relay: missing pipe legs for flow %s", sess.flowKey)
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

	var wg sync.WaitGroup
	var writeMu sync.Mutex
	wg.Add(2)
	go func() {
		defer wg.Done()
		streamFrames(&writeMu, conn, reqR, dirRequest)
	}()
	go func() {
		defer wg.Done()
		streamFrames(&writeMu, conn, resR, dirResponse)
	}()
	wg.Wait()
}

func streamFrames(writeMu *sync.Mutex, conn net.Conn, r io.ReadCloser, dir byte) {
	buf := make([]byte, 32*1024)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			writeMu.Lock()
			werr := writeFrame(conn, dir, buf[:n])
			writeMu.Unlock()
			if werr != nil {
				return
			}
		}
		if err != nil {
			return
		}
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
