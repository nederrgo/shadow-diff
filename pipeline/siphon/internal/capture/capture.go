package capture

import (
	"context"
	"encoding/binary"
	"log"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/gopacket"
	"github.com/google/gopacket/layers"
	"github.com/google/gopacket/tcpassembly"
	"github.com/shadow-diff/siphon/internal/assembly"
	"github.com/shadow-diff/siphon/internal/config"
	"github.com/shadow-diff/siphon/internal/pbpacket"
	"github.com/shadow-diff/siphon/internal/session"
	"google.golang.org/grpc"
)

const defaultPCAPListenAddr = "127.0.0.1:9990"

// GRPCReadyFile signals NetObserv sidecar that the collector is listening (shared emptyDir).
const GRPCReadyFile = "/var/run/siphon/grpc-ready"

func markGRPCReady() {
	_ = os.MkdirAll("/var/run/siphon", 0o755)
	_ = os.WriteFile(GRPCReadyFile, []byte("1"), 0o644)
}

func clearGRPCReady() {
	_ = os.Remove(GRPCReadyFile)
}

type CaptureManager struct {
	pbpacket.UnimplementedCollectorServer
	mu             sync.Mutex
	cfgMgr         *config.Manager
	sessionMap     *session.SessionMap
	assembler      *tcpassembly.Assembler
	streamPool     *tcpassembly.StreamPool
	factory        *assembly.StreamFactory
	pcapAddr       string
	listener       net.Listener
	grpcServer     *grpc.Server
	framesRead     uint64
	packetsMatched uint64
	flowTrace      *flowTracer
	running        bool
	stopChan       chan struct{}
	wg             sync.WaitGroup
}

func NewCaptureManager(cfgMgr *config.Manager, sessionMap *session.SessionMap, factory *assembly.StreamFactory) *CaptureManager {
	pool := tcpassembly.NewStreamPool(factory)
	assembler := tcpassembly.NewAssembler(pool)

	cm := &CaptureManager{
		cfgMgr:     cfgMgr,
		sessionMap: sessionMap,
		assembler:  assembler,
		streamPool: pool,
		factory:    factory,
		flowTrace:  newFlowTracer(),
		stopChan:   make(chan struct{}),
	}
	SetDebugTracer(cm.flowTrace)
	return cm
}

func (cm *CaptureManager) Start(pcapAddr string) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	if cm.running {
		return nil
	}
	if pcapAddr == "" {
		pcapAddr = defaultPCAPListenAddr
	}
	cm.pcapAddr = pcapAddr
	cm.running = true
	cm.stopChan = make(chan struct{})

	log.Printf("Starting gRPC packet collector on %s", pcapAddr)

	cm.wg.Add(1)
	go cm.listenLoop()

	cm.wg.Add(1)
	go cm.flushLoop()

	cm.wg.Add(1)
	go cm.flushOlderLoop()

	return nil
}

func (cm *CaptureManager) Stop() {
	cm.mu.Lock()
	if !cm.running {
		cm.mu.Unlock()
		return
	}
	cm.running = false
	close(cm.stopChan)
	gs := cm.grpcServer
	cm.mu.Unlock()

	if gs != nil {
		gs.GracefulStop()
	}

	cm.mu.Lock()
	if cm.listener != nil {
		_ = cm.listener.Close()
		cm.listener = nil
	}
	cm.grpcServer = nil
	cm.mu.Unlock()

	cm.wg.Wait()
	clearGRPCReady()
	log.Println("Capture stopped.")
}

func (cm *CaptureManager) Status() (pcapAddr string, frames uint64, packets uint64) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	return cm.pcapAddr, atomic.LoadUint64(&cm.framesRead), atomic.LoadUint64(&cm.packetsMatched)
}

func (cm *CaptureManager) setServeState(ln net.Listener, gs *grpc.Server) {
	cm.mu.Lock()
	cm.listener = ln
	cm.grpcServer = gs
	cm.mu.Unlock()
}

func (cm *CaptureManager) clearServeState() {
	cm.mu.Lock()
	cm.listener = nil
	cm.grpcServer = nil
	cm.mu.Unlock()
}

func (cm *CaptureManager) sleepOrStop(d time.Duration) bool {
	select {
	case <-cm.stopChan:
		return false
	case <-time.After(d):
		return true
	}
}

func (cm *CaptureManager) listenLoop() {
	defer cm.wg.Done()

	for {
		select {
		case <-cm.stopChan:
			return
		default:
		}

		ln, err := net.Listen("tcp", cm.pcapAddr)
		if err != nil {
			log.Printf("gRPC listen on %s failed: %v; retry in 2s", cm.pcapAddr, err)
			if !cm.sleepOrStop(2 * time.Second) {
				return
			}
			continue
		}

		gs := grpc.NewServer()
		pbpacket.RegisterCollectorServer(gs, cm)
		cm.setServeState(ln, gs)
		markGRPCReady()
		log.Printf("gRPC collector ready on %s", cm.pcapAddr)

		err = gs.Serve(ln)
		cm.clearServeState()

		if err != nil {
			select {
			case <-cm.stopChan:
				return
			default:
				log.Printf("gRPC serve on %s ended: %v; retry in 2s", cm.pcapAddr, err)
				if !cm.sleepOrStop(2 * time.Second) {
					return
				}
			}
		}
	}
}

// Send implements pbpacket.CollectorServer. Uses req.Pcap.Value directly (no anypb.UnmarshalTo).
func (cm *CaptureManager) Send(_ context.Context, req *pbpacket.Packet) (*pbpacket.CollectorReply, error) {
	if req == nil || req.Pcap == nil || len(req.Pcap.Value) == 0 {
		return &pbpacket.CollectorReply{}, nil
	}
	frame, ci, ok := decodePcapAny(req.Pcap.Value)
	if !ok || len(frame) == 0 {
		return &pbpacket.CollectorReply{}, nil
	}
	cm.processPacket(frame, ci)
	return &pbpacket.CollectorReply{}, nil
}

// decodePcapAny strips a 16-byte pcap per-packet record header when incl_len matches.
func decodePcapAny(data []byte) (frame []byte, ci gopacket.CaptureInfo, ok bool) {
	if len(data) == 0 {
		return nil, gopacket.CaptureInfo{}, false
	}
	if len(data) < 16 {
		return data, gopacket.CaptureInfo{Timestamp: time.Now()}, true
	}
	tsSec := binary.LittleEndian.Uint32(data[0:4])
	tsUsec := binary.LittleEndian.Uint32(data[4:8])
	inclLen := binary.LittleEndian.Uint32(data[8:12])
	if int(inclLen) == len(data)-16 {
		ts := time.Unix(int64(tsSec), int64(tsUsec)*1000)
		return data[16:], gopacket.CaptureInfo{Timestamp: ts}, true
	}
	return data, gopacket.CaptureInfo{Timestamp: time.Now()}, true
}

func decodePacket(data []byte) gopacket.Packet {
	pkt := gopacket.NewPacket(data, layers.LinkTypeEthernet, gopacket.Default)
	if pkt.Layer(layers.LayerTypeIPv4) != nil {
		return pkt
	}
	if len(data) > 0 && data[0]>>4 == 4 {
		return gopacket.NewPacket(data, layers.LayerTypeIPv4, gopacket.Default)
	}
	return pkt
}

func (cm *CaptureManager) processPacket(data []byte, ci gopacket.CaptureInfo) {
	atomic.AddUint64(&cm.framesRead, 1)

	packet := decodePacket(data)

	ip4Layer := packet.Layer(layers.LayerTypeIPv4)
	if ip4Layer == nil {
		return
	}
	ip4 := ip4Layer.(*layers.IPv4)
	srcIP := ip4.SrcIP.String()
	dstIP := ip4.DstIP.String()

	tcpLayer := packet.Layer(layers.LayerTypeTCP)
	if tcpLayer == nil {
		return
	}
	tcp := tcpLayer.(*layers.TCP)
	srcPort := uint16(tcp.SrcPort)
	dstPort := uint16(tcp.DstPort)

	if !packetMatchesCapture(cm.cfgMgr, srcIP, dstIP, int(srcPort), int(dstPort)) {
		return
	}

	atomic.AddUint64(&cm.packetsMatched, 1)

	sampleRate := cm.cfgMgr.GetConfig().SampleRate
	if sampleRate <= 0 {
		sampleRate = 100
	}
	if !cm.sessionMap.GetOrDecide(srcIP, srcPort, dstIP, dstPort, sampleRate) {
		return
	}

	cm.flowTrace.log(cm.cfgMgr, srcIP, dstIP, srcPort, dstPort, tcp)

	ts := ci.Timestamp
	if ts.IsZero() {
		ts = time.Now()
	}
	netFlow := packet.NetworkLayer().NetworkFlow()
	cm.assembler.AssembleWithTimestamp(netFlow, tcp, ts)
}

func (cm *CaptureManager) flushOlderLoop() {
	defer cm.wg.Done()
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-cm.stopChan:
			return
		case <-ticker.C:
			cm.assembler.FlushOlderThan(time.Now().Add(-2 * time.Minute))
		}
	}
}

func (cm *CaptureManager) flushLoop() {
	defer cm.wg.Done()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-cm.stopChan:
			return
		case <-ticker.C:
			cm.factory.ProcessEgressIdleTruncation()
			// ponytail: 500ms idle closed streams too early for multi-segment HTTP responses; use 10s
			cm.assembler.FlushWithOptions(tcpassembly.FlushOptions{
				T: time.Now().Add(-10 * time.Second),
			})
		}
	}
}
