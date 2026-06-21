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
	log.Printf("siphon debug: egress relay connected flow=%s dest=%s", sess.flowKey, dest)

	var wg sync.WaitGroup
	var writeMu sync.Mutex
	wg.Add(2)
	go func() {
		defer wg.Done()
		streamFrames(&writeMu, conn, reqR, dirRequest, sess.flowKey)
	}()
	go func() {
		defer wg.Done()
		streamFrames(&writeMu, conn, resR, dirResponse, sess.flowKey)
	}()
	wg.Wait()
}

func streamFrames(writeMu *sync.Mutex, conn net.Conn, r io.ReadCloser, dir byte, flowKey string) {
	buf := make([]byte, 32*1024)
	var logged sync.Once
	for {
		n, err := r.Read(buf)
		if n > 0 {
			logged.Do(func() {
				dirName := "response"
				if dir == dirRequest {
					dirName = "request"
				}
				log.Printf("siphon debug: egress relay frame flow=%s dir=%s nbytes=%d", flowKey, dirName, n)
			})
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
