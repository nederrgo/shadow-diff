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
func runRelay(sess *Session, poolMgr *forward.PoolManager) {
	target := sess.Target
	if target == nil || target.RecorderHost == "" {
		log.Printf("egress relay: recorder_host not configured for flow %s", sess.flowKey)
		return
	}

	deadline := time.After(2 * time.Minute)
	for sess.reqR == nil {
		select {
		case <-deadline:
			log.Printf("egress relay: timed out waiting for request stream %s", sess.flowKey)
			return
		case <-time.After(2 * time.Millisecond):
		}
	}
	for sess.resR == nil {
		select {
		case <-deadline:
			log.Printf("egress relay: timed out waiting for response stream %s", sess.flowKey)
			return
		case <-time.After(2 * time.Millisecond):
		}
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
	sess.setConn(conn)

	var wg sync.WaitGroup
	var writeMu sync.Mutex
	wg.Add(2)
	go func() {
		defer wg.Done()
		streamFrames(&writeMu, conn, sess.reqR, dirRequest)
	}()
	go func() {
		defer wg.Done()
		streamFrames(&writeMu, conn, sess.resR, dirResponse)
	}()
	wg.Wait()
	_ = conn.Close()
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
