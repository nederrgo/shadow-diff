package capture

import (
	"fmt"
	"log"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/gopacket"
	"github.com/google/gopacket/afpacket"
	"github.com/google/gopacket/layers"
	"github.com/google/gopacket/pcap"
	"github.com/google/gopacket/tcpassembly"
	"github.com/shadow-diff/siphon/internal/assembly"
	"github.com/shadow-diff/siphon/internal/config"
	"github.com/shadow-diff/siphon/internal/session"
	"golang.org/x/net/bpf"
)

// captureSnapLen is the snap length for BPF compilation and TPacket frames.
// Must match OptFrameSize; keep moderate (8192) — very large values often fail
// afpacket.NewTPacket on Kind nodes.
const captureSnapLen = 8192

type CaptureManager struct {
	mu             sync.Mutex
	cfgMgr         *config.Manager
	sessionMap     *session.SessionMap
	assembler      *tcpassembly.Assembler
	streamPool     *tcpassembly.StreamPool
	factory        *assembly.StreamFactory
	interfaces     []string
	framesRead     uint64
	packetsMatched uint64
	running        bool
	stopChan       chan struct{}
	wg             sync.WaitGroup
	handles        map[string]*afpacket.TPacket
}

func NewCaptureManager(cfgMgr *config.Manager, sessionMap *session.SessionMap, factory *assembly.StreamFactory) *CaptureManager {
	pool := tcpassembly.NewStreamPool(factory)
	assembler := tcpassembly.NewAssembler(pool)

	return &CaptureManager{
		cfgMgr:     cfgMgr,
		sessionMap: sessionMap,
		assembler:  assembler,
		streamPool: pool,
		factory:    factory,
		stopChan:   make(chan struct{}),
		handles:    make(map[string]*afpacket.TPacket),
	}
}

func (cm *CaptureManager) Start(interfaceEnv string) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	if cm.running {
		return nil
	}

	var targets []string
	if interfaceEnv == "any" || interfaceEnv == "" {
		targets = selectCaptureInterfaces()
	} else {
		targets = []string{interfaceEnv}
	}

	if len(targets) == 0 {
		return fmt.Errorf("no active network interfaces found to capture on")
	}

	cm.interfaces = targets
	cm.running = true
	cm.stopChan = make(chan struct{})

	log.Printf("Starting packet capture on interfaces: %v", targets)

	for _, iface := range targets {
		cm.wg.Add(1)
		go cm.captureLoop(iface)
	}

	// Relaxed Return Path Assembly Flushing:
	// To prevent sequence-number gaps (which happen often in mirrored/sniffer modes) from stalling
	// the reassembly and accumulating memory, we run a periodic flushing ticker on the assembler.
	// This forces the assembler to ignore sequence checks and flush any pending bytes immediately.
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

	// Close all active handles to unblock NextPacket reads during shutdown
	for _, handle := range cm.handles {
		handle.Close()
	}
	cm.mu.Unlock()

	cm.wg.Wait()
	log.Println("Capture stopped on all interfaces.")
}

// selectCaptureInterfaces picks Kind-friendly interfaces first (cni0, eth0)
// instead of opening a TPacket on every veth (which often fails or wastes FDs).
func selectCaptureInterfaces() []string {
	ifaces, err := net.Interfaces()
	if err != nil {
		log.Printf("list interfaces: %v", err)
		return nil
	}
	byName := make(map[string]net.Interface, len(ifaces))
	for _, iface := range ifaces {
		byName[iface.Name] = iface
	}
	var preferred = []string{"cni0", "docker0", "flannel.1", "eth0", "ens160", "enp0s8"}
	seen := make(map[string]bool)
	var targets []string
	add := func(name string) {
		if seen[name] {
			return
		}
		iface, ok := byName[name]
		if !ok {
			return
		}
		if (iface.Flags&net.FlagLoopback) != 0 || (iface.Flags&net.FlagUp) == 0 {
			return
		}
		seen[name] = true
		targets = append(targets, name)
	}
	for _, name := range preferred {
		add(name)
	}
	// Kind/CNI bridges (e.g. br-xxxxxxxx) carry pod-to-pod traffic when cni0 is absent.
	hasBridge := false
	for _, iface := range ifaces {
		if strings.HasPrefix(iface.Name, "br-") {
			hasBridge = true
			add(iface.Name)
		}
	}
	// Kindnet without cni0/br: pod traffic appears on veth* pairs (your node only has eth0 + veth*).
	if !seen["cni0"] && !hasBridge {
		for _, iface := range ifaces {
			if strings.HasPrefix(iface.Name, "veth") {
				add(iface.Name)
			}
		}
	}
	if len(targets) > 0 {
		log.Printf("Capture interfaces selected: %v", targets)
		return targets
	}
	for _, iface := range ifaces {
		if (iface.Flags & net.FlagLoopback) != 0 {
			continue
		}
		if (iface.Flags & net.FlagUp) == 0 {
			continue
		}
		targets = append(targets, iface.Name)
	}
	return targets
}

func (cm *CaptureManager) Status() (interfaces []string, frames uint64, packets uint64) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	return cm.interfaces, atomic.LoadUint64(&cm.framesRead), atomic.LoadUint64(&cm.packetsMatched)
}

func (cm *CaptureManager) captureLoop(ifaceName string) {
	defer cm.wg.Done()

	// High-performance afpacket capture handle with shared captureSnapLen configuration
	handle, err := afpacket.NewTPacket(
		afpacket.OptInterface(ifaceName),
		afpacket.OptFrameSize(captureSnapLen),
		afpacket.OptBlockSize(captureSnapLen * 128),
		afpacket.OptNumBlocks(128),
	)
	if err != nil {
		log.Printf("Error opening afpacket on interface %s: %v", ifaceName, err)
		return
	}
	defer handle.Close()

	cm.mu.Lock()
	if !cm.running {
		cm.mu.Unlock()
		return
	}
	cm.handles[ifaceName] = handle
	cm.mu.Unlock()

	defer func() {
		cm.mu.Lock()
		delete(cm.handles, ifaceName)
		cm.mu.Unlock()
	}()

	// Install initial BPF filter if configuration targets exist
	if err := cm.installBPFFilter(handle); err != nil {
		log.Printf("BPF initial filter compilation/application skipped or failed on %s: %v", ifaceName, err)
	}

	packetSource := gopacket.NewPacketSource(handle, layers.LinkTypeEthernet)
	packetSource.NoCopy = true

	log.Printf("Capture started on interface: %s", ifaceName)

	for {
		select {
		case <-cm.stopChan:
			return
		default:
		}

		packet, err := packetSource.NextPacket()
		if err != nil {
			continue
		}

		atomic.AddUint64(&cm.framesRead, 1)

		// BPF filters for tcp and host, so all packets received here are TCP target packets.
		// We still parse IPv4/TCP layers to pass structured flows and timestamps to the assembler.
		var srcIP, dstIP string
		if ip4Layer := packet.Layer(layers.LayerTypeIPv4); ip4Layer != nil {
			ip4 := ip4Layer.(*layers.IPv4)
			srcIP = ip4.SrcIP.String()
			dstIP = ip4.DstIP.String()
		} else {
			continue
		}

		tcpLayer := packet.Layer(layers.LayerTypeTCP)
		if tcpLayer == nil {
			continue
		}
		tcp := tcpLayer.(*layers.TCP)
		srcPort := uint16(tcp.SrcPort)
		dstPort := uint16(tcp.DstPort)

		// Increment packets_matched as BPF filter matches target IPv4 TCP traffic
		atomic.AddUint64(&cm.packetsMatched, 1)

		// Sticky Sampling Decision using the global sample rate
		sampleRate := cm.cfgMgr.GetConfig().SampleRate
		if sampleRate <= 0 {
			sampleRate = 100
		}

		if !cm.sessionMap.GetOrDecide(srcIP, srcPort, dstIP, dstPort, sampleRate) {
			if dstPort == 8080 || srcPort == 8080 {
				log.Printf("siphon debug: tcp %s:%d -> %s:%d iface=%s dropped by sampling (rate=%d)",
					srcIP, srcPort, dstIP, dstPort, ifaceName, sampleRate)
			}
			continue
		}

		if dstPort == 8080 || srcPort == 8080 {
			log.Printf("siphon debug: tcp %s:%d -> %s:%d iface=%s -> assembler",
				srcIP, srcPort, dstIP, dstPort, ifaceName)
		}

		// Feed packet into TCP assembler
		netFlow := packet.NetworkLayer().NetworkFlow()
		cm.assembler.AssembleWithTimestamp(netFlow, tcp, packet.Metadata().Timestamp)
	}
}

func (cm *CaptureManager) installBPFFilter(handle *afpacket.TPacket) error {
	cfg := cm.cfgMgr.GetConfig()
	filter, err := BuildBPFFilter(cfg)
	if err != nil {
		return fmt.Errorf("build filter: %w", err)
	}

	if err := compileAndAttachBPF(handle, filter); err != nil {
		return fmt.Errorf("set filter: %w", err)
	}

	log.Printf("BPF filter successfully compiled and applied: %s", filter)
	return nil
}

func (cm *CaptureManager) ApplyBPFFilter() error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	if !cm.running {
		return nil
	}

	cfg := cm.cfgMgr.GetConfig()
	filter, err := BuildBPFFilter(cfg)
	if err != nil {
		return fmt.Errorf("apply BPF: build filter failed: %w", err)
	}

	for iface, handle := range cm.handles {
		if err := compileAndAttachBPF(handle, filter); err != nil {
			return fmt.Errorf("apply BPF: failed to set filter on %s: %w", iface, err)
		}
		log.Printf("BPF filter dynamically updated on interface %s: %s", iface, filter)
	}

	return nil
}

func compileAndAttachBPF(handle *afpacket.TPacket, filter string) error {
	pcapInsts, err := pcap.CompileBPFFilter(layers.LinkTypeEthernet, captureSnapLen, filter)
	if err != nil {
		return fmt.Errorf("compile: %w", err)
	}

	rawInsts := make([]bpf.RawInstruction, len(pcapInsts))
	for i, inst := range pcapInsts {
		rawInsts[i] = bpf.RawInstruction{
			Op: inst.Code,
			Jt: inst.Jt,
			Jf: inst.Jf,
			K:  inst.K,
		}
	}

	if err := handle.SetBPF(rawInsts); err != nil {
		return fmt.Errorf("attach: %w", err)
	}

	return nil
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
			// Flush buffered streams to ignore sequence gaps and force immediate delivery of reassembled segments
			cm.assembler.FlushWithOptions(tcpassembly.FlushOptions{
				T: time.Now().Add(-500 * time.Millisecond),
			})
		}
	}
}
