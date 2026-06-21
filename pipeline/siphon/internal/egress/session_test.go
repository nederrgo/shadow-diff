package egress

import (
	"net"
	"testing"
	"time"

	"github.com/google/gopacket/tcpassembly"
	"github.com/shadow-diff/siphon/internal/config"
	"github.com/shadow-diff/siphon/internal/forward"
)

func TestRemove_doesNotCloseRecorderConn(t *testing.T) {
	st := NewSessionStore(forward.NewPoolManager(8, time.Second))

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	acceptDone := make(chan net.Conn, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		acceptDone <- c
	}()

	cli, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()

	sess := &Session{flowKey: "test-flow"}
	allocBothPipes(sess, st)
	st.sessions["test-flow"] = sess

	st.Remove("test-flow")

	if err := cli.SetWriteDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := cli.Write([]byte("still-open")); err != nil {
		t.Fatalf("Remove closed recorder conn early: %v", err)
	}

	select {
	case srv := <-acceptDone:
		_ = srv.Close()
	case <-time.After(2 * time.Second):
		t.Fatal("accept timed out")
	}
}

func TestSession_tryStartRelay_resetsForKeepAlive(t *testing.T) {
	sess := &Session{}
	if !sess.tryStartRelay() {
		t.Fatal("first tryStartRelay should succeed")
	}
	if sess.tryStartRelay() {
		t.Fatal("second tryStartRelay should fail while running")
	}
	sess.clearRelayRunning()
	if !sess.tryStartRelay() {
		t.Fatal("tryStartRelay after clear should succeed for keep-alive respawn")
	}
}

func TestGetOrCreate_recyclesRequestLegAndRespawnsRelay(t *testing.T) {
	// Relay starts on first reassembled bytes, not at GetOrCreate.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer conn.Close()
				buf := make([]byte, 4096)
				for {
					if _, err := conn.Read(buf); err != nil {
						return
					}
				}
			}(c)
		}
	}()

	st := NewSessionStore(forward.NewPoolManager(8, time.Second))
	target := &config.SiphonTarget{RecorderHost: ln.Addr().String()}

	flowKey := "10.0.0.1:1234-10.0.0.2:80"
	reqStream := st.GetOrCreate(flowKey, true, target).(*pipeStream)
	reqStream.Reassembled([]tcpassembly.Reassembly{{Bytes: []byte("GET / HTTP/1.1\r\n\r\n")}})
	time.Sleep(100 * time.Millisecond)

	sess := st.sessions[flowKey]
	sess.mu.Lock()
	if !sess.relayRunning {
		sess.mu.Unlock()
		t.Fatal("relay should be running after request bytes")
	}
	sess.mu.Unlock()

	oldReq := sess.reqS
	sess.markLegClosed(true)

	_ = st.GetOrCreate(flowKey, true, target)
	if sess.reqS == nil || sess.reqS == oldReq {
		t.Fatal("expected fresh request pipe after leg recycle")
	}

	time.Sleep(50 * time.Millisecond)
	sess.mu.Lock()
	running := sess.relayRunning
	sess.mu.Unlock()
	if !running {
		t.Fatal("expected relay respawn after recycled request leg")
	}
}

func TestAllocBothPipes_bothLegsReady(t *testing.T) {
	st := NewSessionStore(forward.NewPoolManager(8, time.Second))
	sess := &Session{}
	allocBothPipes(sess, st)
	if sess.reqR == nil || sess.resR == nil || sess.reqS == nil || sess.resS == nil {
		t.Fatal("allocBothPipes must create both legs immediately")
	}
}
