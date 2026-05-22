package assembly

import (
	"context"
	"fmt"
	"log"
	"net"
	"sync/atomic"
	"time"

	"github.com/google/gopacket"
	"github.com/google/gopacket/tcpassembly"
	"github.com/shadow-diff/siphon/internal/config"
	"github.com/shadow-diff/siphon/internal/forward"
)

type StreamFactory struct {
	cfgMgr       *config.Manager
	poolMgr      *forward.PoolManager
	forwardCount *uint64 // requests_forwarded metric pointer
}

func NewStreamFactory(cfgMgr *config.Manager, poolMgr *forward.PoolManager, forwardCount *uint64) *StreamFactory {
	return &StreamFactory{
		cfgMgr:       cfgMgr,
		poolMgr:      poolMgr,
		forwardCount: forwardCount,
	}
}

func (f *StreamFactory) New(netFlow, tcpFlow gopacket.Flow) tcpassembly.Stream {
	srcIP := netFlow.Src().String()
	dstIP := netFlow.Dst().String()
	srcPortStr := tcpFlow.Src().String()
	dstPortStr := tcpFlow.Dst().String()

	var srcPort, dstPort int
	fmt.Sscanf(srcPortStr, "%d", &srcPort)
	fmt.Sscanf(dstPortStr, "%d", &dstPort)

	// Check if this is a request stream (Client -> Pod)
	if f.cfgMgr.IsTarget(dstIP, dstPort) {
		target, driver, ok := f.cfgMgr.LookupTarget(dstIP, dstPort)
		if ok {
			return &requestStream{
				targetIP:     dstIP,
				targetPort:   dstPort,
				igrisHost:    target.IgrisHost,
				driver:       driver,
				poolMgr:      f.poolMgr,
				forwardCount: f.forwardCount,
			}
		}
	}

	// Check if this is a return stream (Pod -> Client)
	if f.cfgMgr.IsTarget(srcIP, srcPort) {
		return &returnStream{
			srcIP:   srcIP,
			srcPort: srcPort,
		}
	}

	return &discardStream{}
}

type requestStream struct {
	targetIP     string
	targetPort   int
	igrisHost    string
	driver       string
	poolMgr      *forward.PoolManager
	forwardCount *uint64
	conn         net.Conn
	dialErr      error
	dialed       bool
}

func (s *requestStream) Reassembled(reassemblies []tcpassembly.Reassembly) {
	if s.dialErr != nil {
		return
	}

	if !s.dialed {
		s.dialed = true
		dest := fmt.Sprintf("%s:%d", s.igrisHost, s.targetPort)
		pool := s.poolMgr.GetPool(dest)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()

		conn, err := pool.Dial(ctx)
		if err != nil {
			s.dialErr = err
			log.Printf("Failed to dial Igris at %s: %v", dest, err)
			return
		}
		s.conn = conn

		if s.driver == "http_request" {
			log.Println("Reassembled HTTP request")
		} else {
			log.Printf("Reassembled TCP stream on port %d", s.targetPort)
		}
		atomic.AddUint64(s.forwardCount, 1)
	}

	for _, r := range reassemblies {
		if len(r.Bytes) > 0 {
			_, err := s.conn.Write(r.Bytes)
			if err != nil {
				log.Printf("Failed to write to Igris: %v", err)
				_ = s.conn.Close()
				s.dialErr = err
				return
			}
		}
	}
}

func (s *requestStream) ReassemblyComplete() {
	if s.conn != nil {
		_ = s.conn.Close()
	}
}

type returnStream struct {
	srcIP   string
	srcPort int
}

func (s *returnStream) Reassembled(reassemblies []tcpassembly.Reassembly) {
	// Relaxed return path:
	// In passive capture, the sniffer often misses production ACKs on the return leg.
	// Standard reassembly can stall request streams if it blocks on return-path sequence checks.
	// Therefore, we do not buffer or forward return-path data, and the assembler is flushed
	// periodically to bypass sequence number checks and prevent memory accumulation from gaps.
}

func (s *returnStream) ReassemblyComplete() {}

type discardStream struct{}

func (s *discardStream) Reassembled(reassemblies []tcpassembly.Reassembly) {}
func (s *discardStream) ReassemblyComplete()                             {}
