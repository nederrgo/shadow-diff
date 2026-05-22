package capture

import (
	"fmt"
	"log"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/gopacket"
	"github.com/google/gopacket/afpacket"
	"github.com/google/gopacket/layers"
	"github.com/google/gopacket/tcpassembly"
	"github.com/shadow-diff/siphon/internal/assembly"
	"github.com/shadow-diff/siphon/internal/config"
	"github.com/shadow-diff/siphon/internal/session"
)

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
		ifaces, err := net.Interfaces()
		if err != nil {
			return fmt.Errorf("failed to list network interfaces: %w", err)
		}
		for _, iface := range ifaces {
			// Skip loopback and down interfaces
			if (iface.Flags & net.FlagLoopback) != 0 {
				continue
			}
			if (iface.Flags & net.FlagUp) == 0 {
				continue
			}
			targets = append(targets, iface.Name)
		}
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
	cm.mu.Unlock()

	cm.wg.Wait()
	log.Println("Capture stopped on all interfaces.")
}

func (cm *CaptureManager) Status() (interfaces []string, frames uint64, packets uint64) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	return cm.interfaces, atomic.LoadUint64(&cm.framesRead), atomic.LoadUint64(&cm.packetsMatched)
}

func (cm *CaptureManager) captureLoop(ifaceName string) {
	defer cm.wg.Done()

	// High-performance afpacket capture handle
	handle, err := afpacket.NewTPacket(afpacket.OptInterface(ifaceName))
	if err != nil {
		log.Printf("Error opening afpacket on interface %s: %v", ifaceName, err)
		return
	}
	defer handle.Close()

	// Performance phase: push target filter into kernel to reduce userspace load.
	// Example (build from current target_ips / target_ports):
	//   filter := buildBPFFilter(cfg) // "tcp and host 10.244.1.2 and port 80"
	//   if err := handle.SetBPFFilter(filter, 65535); err != nil { ... }

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

		// Userspace filtering: IPv4/IPv6 TCP only, matching target_ips & target_ports
		var srcIP, dstIP string
		if ip4Layer := packet.Layer(layers.LayerTypeIPv4); ip4Layer != nil {
			ip4 := ip4Layer.(*layers.IPv4)
			srcIP = ip4.SrcIP.String()
			dstIP = ip4.DstIP.String()
		} else if ip6Layer := packet.Layer(layers.LayerTypeIPv6); ip6Layer != nil {
			ip6 := ip6Layer.(*layers.IPv6)
			srcIP = ip6.SrcIP.String()
			dstIP = ip6.DstIP.String()
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

		// 1. Userspace Target Filtering
		isRequest := cm.cfgMgr.IsTarget(dstIP, int(dstPort))
		isReturn := cm.cfgMgr.IsTarget(srcIP, int(srcPort))

		if !isRequest && !isReturn {
			continue
		}

		atomic.AddUint64(&cm.packetsMatched, 1)

		// 2. Sticky Sampling Decision
		var sampleRate int
		_, _, ok := cm.cfgMgr.LookupTarget(dstIP, int(dstPort))
		if ok {
			sampleRate = cm.cfgMgr.GetConfig().SampleRate
		} else {
			_, _, ok = cm.cfgMgr.LookupTarget(srcIP, int(srcPort))
			if ok {
				sampleRate = cm.cfgMgr.GetConfig().SampleRate
			} else {
				sampleRate = 100
			}
		}

		if !cm.sessionMap.GetOrDecide(srcIP, srcPort, dstIP, dstPort, sampleRate) {
			continue
		}

		// 3. Feed packet into TCP assembler
		netFlow := packet.NetworkLayer().NetworkFlow()
		cm.assembler.AssembleWithTimestamp(netFlow, tcp, packet.Metadata().Timestamp)
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
