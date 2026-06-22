package egress

import (
	"net"
	"strings"
	"sync/atomic"
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

	st.sessions["test-flow"] = &Session{flowKey: "test-flow"}
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
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	var relays atomic.Int32
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			relays.Add(1)
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
	reqStream.ReassemblyComplete()
	resStream := st.GetOrCreate(flowKey, false, target).(*pipeStream)
	resStream.Reassembled([]tcpassembly.Reassembly{{Bytes: []byte("HTTP/1.1 200 OK\r\n\r\n")}})
	resStream.ReassemblyComplete()
	time.Sleep(150 * time.Millisecond)
	if relays.Load() != 1 {
		t.Fatalf("first relay: got %d recorder connections want 1", relays.Load())
	}

	req2 := st.GetOrCreate(flowKey, true, target).(*pipeStream)
	req2.Reassembled([]tcpassembly.Reassembly{{Bytes: []byte("GET /2 HTTP/1.1\r\n\r\n")}})
	req2.ReassemblyComplete()
	res2 := st.GetOrCreate(flowKey, false, target).(*pipeStream)
	res2.Reassembled([]tcpassembly.Reassembly{{Bytes: []byte("HTTP/1.1 200 OK\r\n\r\n")}})
	res2.ReassemblyComplete()

	time.Sleep(150 * time.Millisecond)
	if relays.Load() != 2 {
		t.Fatalf("second relay: got %d recorder connections want 2", relays.Load())
	}
}

func TestSession_buffersUntilBothLegsClose(t *testing.T) {
	st := NewSessionStore(forward.NewPoolManager(8, time.Second))
	target := &config.SiphonTarget{RecorderHost: "127.0.0.1:9"} // invalid — relay must not consume buffers before assert
	flowKey := "10.0.0.1:1-10.0.0.2:80"

	reqPartial := []byte("POST / HTTP/1.1\r\nContent-Length: 4\r\n\r\nab")
	reqRest := []byte("cd")
	resPartial := []byte("HTTP/1.1 200 OK\r\nContent-Length: 8\r\n\r\nres")
	resRest := []byte("-body")
	reqBytes := append(append([]byte(nil), reqPartial...), reqRest...)
	resBytes := append(append([]byte(nil), resPartial...), resRest...)

	req := st.GetOrCreate(flowKey, true, target).(*pipeStream)
	req.Reassembled([]tcpassembly.Reassembly{{Bytes: reqPartial}})
	sess := st.sessions[flowKey]
	if sess.bothLegsClosed() {
		t.Fatal("partial request alone should not close both legs")
	}

	res := st.GetOrCreate(flowKey, false, target).(*pipeStream)
	res.Reassembled([]tcpassembly.Reassembly{{Bytes: resPartial}})
	if sess.bothLegsClosed() {
		t.Fatal("partial response alone should not close both legs")
	}

	req.Reassembled([]tcpassembly.Reassembly{{Bytes: reqRest}})
	res.Reassembled([]tcpassembly.Reassembly{{Bytes: resRest}})
	req.ReassemblyComplete()
	res.ReassemblyComplete()
	sess.mu.Lock()
	closed := sess.reqClosed && sess.resClosed
	reqLen, resLen := len(sess.reqBuf), len(sess.resBuf)
	sess.mu.Unlock()
	if !closed {
		t.Fatal("both legs should be closed")
	}
	if reqLen != len(reqBytes) || resLen != len(resBytes) {
		t.Fatalf("buffers should remain until relay takes them: req=%d res=%d", reqLen, resLen)
	}
}

func TestPipeStreamPCA248_productionSequence(t *testing.T) {
	st := NewSessionStore(forward.NewPoolManager(8, time.Second))
	target := &config.SiphonTarget{RecorderHost: "127.0.0.1:9"}
	flowKey := "10.244.164.38:33422-10.244.164.37:8080"
	full := []byte("POST /v1/log HTTP/1.1\r\nHost: user-service.prod.internal\r\nUser-Agent: python-requests/2.32.3\r\nAccept-Encoding: gzip, deflate\r\nAccept: */*\r\nConnection: keep-alive\r\nContent-Type: application/json\r\nContent-Length: 58\r\n\r\n{\"status\": \"complete\", \"order_id\": \"e2e-b464291bd69bd4c6\"}")
	resBody := []byte("HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: 15\r\nConnection: keep-alive\r\nX-Pad: " + strings.Repeat("a", 60) + "\r\n\r\n{\"status\":\"ok\"}")
	if len(resBody) < 140 {
		t.Fatalf("response fixture len=%d want >=140", len(resBody))
	}

	res := st.GetOrCreate(flowKey, false, target).(*pipeStream)
	res.Reassembled([]tcpassembly.Reassembly{{Bytes: resBody[:125]}})
	res.Reassembled([]tcpassembly.Reassembly{{Bytes: resBody[125:140]}})
	res.ReassemblyComplete()

	req := st.GetOrCreate(flowKey, true, target).(*pipeStream)
	req.Reassembled([]tcpassembly.Reassembly{{Bytes: full[:190]}})
	req.Reassembled([]tcpassembly.Reassembly{{Bytes: full[190:248]}})
	req.ReassemblyComplete()

	sess := st.sessions[flowKey]
	sess.mu.Lock()
	closed := sess.reqClosed && sess.resClosed
	relay := sess.relayRunning
	sess.mu.Unlock()
	if !closed {
		t.Fatal("production sequence should close both legs after request flush")
	}
	if !relay {
		t.Fatal("production sequence should start relay")
	}
}

func TestPipeStreamPCA248Request(t *testing.T) {
	st := NewSessionStore(forward.NewPoolManager(8, time.Second))
	target := &config.SiphonTarget{RecorderHost: "127.0.0.1:9"}
	flowKey := "10.0.0.1:1-10.0.0.2:8080"
	full := []byte("POST /v1/log HTTP/1.1\r\nHost: user-service.prod.internal\r\nUser-Agent: python-requests/2.32.3\r\nAccept-Encoding: gzip, deflate\r\nAccept: */*\r\nConnection: keep-alive\r\nContent-Type: application/json\r\nContent-Length: 58\r\n\r\n{\"status\": \"complete\", \"order_id\": \"e2e-b464291bd69bd4c6\"}")
	if len(full) != 274 {
		t.Fatalf("fixture len=%d want 274", len(full))
	}

	res := st.GetOrCreate(flowKey, false, target).(*pipeStream)
	res.Reassembled([]tcpassembly.Reassembly{{Bytes: []byte("HTTP/1.1 200 OK\r\nContent-Length: 15\r\n\r\n{\"status\":\"ok\"}")}})
	res.ReassemblyComplete()

	req := st.GetOrCreate(flowKey, true, target).(*pipeStream)
	req.Reassembled([]tcpassembly.Reassembly{{Bytes: full[:190]}})
	req.Reassembled([]tcpassembly.Reassembly{{Bytes: full[190:248]}})

	sess := st.sessions[flowKey]
	sess.mu.Lock()
	closed := sess.reqClosed && sess.resClosed
	relay := sess.relayRunning
	sess.mu.Unlock()
	if !sess.legHTTPComplete(true) {
		t.Fatalf("request should finalize after 248B PCA snap, reqBuf=%q", sess.reqBuf)
	}
	if !closed {
		t.Fatal("both legs should close after truncated request finalizes without FIN")
	}
	if !relay {
		t.Fatal("relay should start when both legs close on truncated PCA request")
	}
}

func TestReassemblyComplete_truncatedRequestTriggersRelay(t *testing.T) {
	st := NewSessionStore(forward.NewPoolManager(8, time.Second))
	target := &config.SiphonTarget{RecorderHost: "127.0.0.1:9"}
	flowKey := "10.0.0.1:1-10.0.0.2:8080"
	trunc := []byte("POST /v1/log HTTP/1.1\r\nHost: x\r\nContent-Type: application/json\r\nContent-Length: 58\r\n\r\n{\"status\": \"complete\", \"order_id\": \"e2e-")

	res := st.GetOrCreate(flowKey, false, target).(*pipeStream)
	res.Reassembled([]tcpassembly.Reassembly{{Bytes: []byte("HTTP/1.1 200 OK\r\nContent-Length: 15\r\n\r\n{\"status\":\"ok\"}")}})
	res.ReassemblyComplete()

	req := st.GetOrCreate(flowKey, true, target).(*pipeStream)
	req.Reassembled([]tcpassembly.Reassembly{{Bytes: trunc}})
	req.ReassemblyComplete()

	sess := st.sessions[flowKey]
	sess.mu.Lock()
	closed := sess.reqClosed && sess.resClosed
	relay := sess.relayRunning
	sess.mu.Unlock()
	if !closed {
		t.Fatal("ReassemblyComplete should finalize truncated request and close both legs")
	}
	if !relay {
		t.Fatal("relay should start after ReassemblyComplete on truncated request")
	}
}

func TestProcessIdleTruncation_afterIdle(t *testing.T) {
	st := NewSessionStore(forward.NewPoolManager(8, time.Second))
	target := &config.SiphonTarget{RecorderHost: "127.0.0.1:9"}
	flowKey := "10.0.0.1:1-10.0.0.2:8080"
	trunc := []byte("POST /v1/log HTTP/1.1\r\nHost: x\r\nContent-Type: application/json\r\nContent-Length: 58\r\n\r\n{\"status\": \"complete\", \"order_id\": \"e2e-")

	res := st.GetOrCreate(flowKey, false, target).(*pipeStream)
	res.Reassembled([]tcpassembly.Reassembly{{Bytes: []byte("HTTP/1.1 200 OK\r\nContent-Length: 15\r\n\r\n{\"status\":\"ok\"}")}})
	res.ReassemblyComplete()

	req := st.GetOrCreate(flowKey, true, target).(*pipeStream)
	req.Reassembled([]tcpassembly.Reassembly{{Bytes: trunc}})

	sess := st.sessions[flowKey]
	sess.mu.Lock()
	sess.reqLastAppendAt = time.Now().Add(-truncationIdleTimeout - time.Millisecond)
	sess.mu.Unlock()

	st.ProcessIdleTruncation()

	sess.mu.Lock()
	closed := sess.reqClosed && sess.resClosed
	relay := sess.relayRunning
	sess.mu.Unlock()
	if !closed {
		t.Fatal("idle truncation should finalize truncated request and close both legs")
	}
	if !relay {
		t.Fatal("relay should start after idle truncation")
	}
}

func TestSession_finalizeRequestOnFIN_closesTruncatedPCARequest(t *testing.T) {
	st := NewSessionStore(forward.NewPoolManager(8, time.Second))
	target := &config.SiphonTarget{RecorderHost: "127.0.0.1:9"}
	flowKey := "10.0.0.1:1-10.0.0.2:8080"

	res := st.GetOrCreate(flowKey, false, target).(*pipeStream)
	res.Reassembled([]tcpassembly.Reassembly{{Bytes: []byte("HTTP/1.1 200 OK\r\nContent-Length: 15\r\n\r\n{\"status\":\"ok\"}")}})
	res.ReassemblyComplete()

	reqTrunc := []byte("POST /v1/log HTTP/1.1\r\nHost: x\r\nContent-Type: application/json\r\nContent-Length: 58\r\n\r\n{\"status\": \"complete\", \"order_id\": \"e2e-")
	req := st.GetOrCreate(flowKey, true, target).(*pipeStream)
	req.Reassembled([]tcpassembly.Reassembly{{Bytes: reqTrunc}})
	req.ReassemblyComplete()

	sess := st.sessions[flowKey]
	sess.mu.Lock()
	closed := sess.reqClosed && sess.resClosed
	sess.mu.Unlock()
	if !closed {
		t.Fatal("truncated request should finalize on FIN and close both legs")
	}
}
