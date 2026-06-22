package capture

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/shadow-diff/siphon/internal/assembly"
	"github.com/shadow-diff/siphon/internal/config"
	"github.com/shadow-diff/siphon/internal/egress"
	"github.com/shadow-diff/siphon/internal/forward"
	"github.com/shadow-diff/siphon/internal/pbpacket"
	"github.com/shadow-diff/siphon/internal/session"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/types/known/anypb"
)

func TestCaptureManager_grpcSendIncrementsFrames(t *testing.T) {
	cfgMgr := config.NewManager()
	cfgMgr.Update(config.SiphonConfig{
		SampleRate: 100,
		Targets: []config.SiphonTarget{{
			TargetIPs:   []string{"10.0.0.1"},
			TargetPorts: []int{80},
		}},
	})

	sessionMap := session.NewSessionMap(time.Minute, 1000)
	poolMgr := forward.NewPoolManager(8, time.Second)
	egressStore := egress.NewSessionStore(poolMgr)
	var fwd uint64
	factory := assembly.NewStreamFactory(cfgMgr, poolMgr, egressStore, &fwd)
	cm := NewCaptureManager(cfgMgr, sessionMap, factory)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	if err := cm.Start(addr); err != nil {
		t.Fatal(err)
	}
	defer cm.Stop()

	// ponytail: brief wait for listenLoop to bind; frames_read stays 0 until Send.
	time.Sleep(200 * time.Millisecond)

	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	client := pbpacket.NewCollectorClient(conn)
	frame := minimalTCPFrame([4]byte{1, 2, 3, 4}, [4]byte{10, 0, 0, 1}, 12345, 80)
	payload := wrapPcapRecord(frame)

	_, err = client.Send(context.Background(), &pbpacket.Packet{
		Pcap: &anypb.Any{Value: payload},
	})
	if err != nil {
		t.Fatal(err)
	}

	_, frames, matched := cm.Status()
	if frames < 1 {
		t.Fatalf("frames_read = %d want >= 1", frames)
	}
	if matched < 1 {
		t.Fatalf("packets_matched = %d want >= 1", matched)
	}
}

func TestCaptureManager_grpcStopExitsCleanly(t *testing.T) {
	cfgMgr := config.NewManager()
	sessionMap := session.NewSessionMap(time.Minute, 1000)
	poolMgr := forward.NewPoolManager(8, time.Second)
	egressStore := egress.NewSessionStore(poolMgr)
	var fwd uint64
	factory := assembly.NewStreamFactory(cfgMgr, poolMgr, egressStore, &fwd)
	cm := NewCaptureManager(cfgMgr, sessionMap, factory)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	if err := cm.Start(addr); err != nil {
		t.Fatal(err)
	}

	done := make(chan struct{})
	go func() {
		cm.Stop()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop did not return within 5s")
	}
}
