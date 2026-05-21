package capture

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/gopacket"
	"github.com/google/gopacket/layers"
	"github.com/google/gopacket/tcpassembly"

	"github.com/shadow-diff/siphon/internal/config"
)

// PacketHandler feeds TCP packets into reassembly.
type PacketHandler interface {
	HandlePacket(packet gopacket.Packet)
	FlushOlderThan(time.Time)
}

// Stats tracks capture counters.
type Stats struct {
	FramesRead      atomic.Uint64 // raw frames from AF_PACKET
	FramesTCP       atomic.Uint64 // decoded as IP+TCP
	FramesUnmatched atomic.Uint64 // TCP decoded but not dst target IP:port
	Packets         atomic.Uint64 // matched target IP+port filter
}

// captureHandle is the minimal interface the engine needs from any capture source.
type captureHandle interface {
	readRaw() ([]byte, error)
	decode([]byte) gopacket.Packet
	ifaceName() string
	Close()
}

// Engine manages packet capture with hot reload.
type Engine struct {
	log          *slog.Logger
	handler      PacketHandler
	assembler    *AssemblerHandler
	stats        *Stats
	interfaceEnv string

	mu          sync.Mutex
	reloading   bool
	cancel      context.CancelFunc
	handles     []captureHandle
	interfaces  []string
	bpfFilter   string
	targetIPs   map[string]struct{}
	targetPorts map[int]struct{}
}

func NewEngine(log *slog.Logger, handler PacketHandler, assembler *AssemblerHandler, stats *Stats) *Engine {
	if log == nil {
		log = slog.Default()
	}
	return &Engine{
		log:          log,
		handler:      handler,
		assembler:    assembler,
		interfaceEnv: InterfaceEnv(),
		stats:        stats,
	}
}

// FlushStale closes TCP streams older than the given time.
func (e *Engine) FlushStale(olderThan time.Time) {
	if e.assembler != nil {
		e.assembler.FlushOlderThan(olderThan)
	}
}

// StatsSnapshot for status API.
type StatsSnapshot struct {
	BPFFilter   string   `json:"bpf_filter"`
	Interfaces  []string `json:"interfaces"`
	FramesRead      uint64 `json:"frames_read"`
	FramesTCP       uint64 `json:"frames_tcp"`
	FramesUnmatched uint64 `json:"frames_unmatched"`
	Packets         uint64 `json:"packets"`
}

func (e *Engine) Snapshot() StatsSnapshot {
	e.mu.Lock()
	defer e.mu.Unlock()
	var framesRead, framesTCP, framesUnmatched, pkts uint64
	if e.stats != nil {
		framesRead = e.stats.FramesRead.Load()
		framesTCP = e.stats.FramesTCP.Load()
		framesUnmatched = e.stats.FramesUnmatched.Load()
		pkts = e.stats.Packets.Load()
	}
	ifaces := append([]string(nil), e.interfaces...)
	return StatsSnapshot{
		BPFFilter:       e.bpfFilter,
		Interfaces:      ifaces,
		FramesRead:      framesRead,
		FramesTCP:       framesTCP,
		FramesUnmatched: framesUnmatched,
		Packets:         pkts,
	}
}

// Reload applies config and restarts capture loops.
func (e *Engine) Reload(ctx context.Context, payload config.Payload) error {
	e.mu.Lock()
	if e.reloading {
		e.mu.Unlock()
		return ErrReloadInProgress
	}
	e.reloading = true
	e.mu.Unlock()

	defer func() {
		e.mu.Lock()
		e.reloading = false
		e.mu.Unlock()
	}()

	e.stopLocked()

	ips, ports := payload.UnionCaptureTargets()
	if len(ips) == 0 {
		e.log.Info("No capture targets; idle")
		return nil
	}
	filter, err := BuildBPFFilter(ips, ports)
	if err != nil {
		return err
	}

	explicit, multi := ResolveInterfaceMode(e.interfaceEnv)
	var ifaces []string
	var handles []captureHandle
	if multi {
		// AF_PACKET ifindex=0 (SOCK_RAW or SOCK_DGRAM) does not deliver frames in WSL2/Kind
		// container environments.  Instead we identify the veth that serves each target IP by
		// consulting /proc/net/arp, then open one SOCK_RAW socket per veth.  The target pod's
		// veth is stable across curl runs — only the curl pod's veth changes per request.
		listed, lerr := FindIfacesForIPs(ips)
		if lerr != nil || len(listed) == 0 {
			// ARP lookup empty means the pods haven't been reached yet.
			// Fall back to all active interfaces so we don't silently drop the first request.
			e.log.Warn("ARP lookup for target IPs empty; falling back to all veth interfaces",
				"ips", ips, "arp_err", lerr)
			listed, lerr = ListCaptureInterfaces()
			if lerr != nil {
				return lerr
			}
		}
		if len(listed) == 0 {
			e.log.Warn("No active interfaces found; capture idle")
			return nil
		}
		e.log.Info("Binding capture to target veths (ARP lookup)", "ifaces", listed)
		ifaces = listed
		for _, iface := range listed {
			// Use raw recvfrom socket instead of TPacket ring — TPacket poll() does
			// not wake up reliably in WSL2/Kind environments.
			h, herr := newRawSockHandle(iface)
			if herr != nil {
				e.log.Warn("Skip interface", "iface", iface, "err", herr)
				continue
			}
			handles = append(handles, h)
		}
	} else {
		ifaces = []string{explicit}
		h, err := newPacketHandle(explicit)
		if err != nil {
			return err
		}
		h.attachKernelBPF(e.log, ips, ports)
		handles = []captureHandle{h}
	}

	runCtx, cancel := context.WithCancel(ctx)
	ipSet, portSet := TargetSets(ips, ports)

	e.mu.Lock()
	e.cancel = cancel
	e.handles = handles
	e.interfaces = ifaces
	e.bpfFilter = filter
	e.targetIPs = ipSet
	e.targetPorts = portSet
	e.mu.Unlock()

	for _, h := range handles {
		go e.readLoop(runCtx, h)
	}

	e.log.Info("Capture started", "filter", filter, "interfaces", ifaces)
	return nil
}

func (e *Engine) Stop() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.stopLocked()
}

func (e *Engine) stopLocked() {
	if e.cancel != nil {
		e.cancel()
		e.cancel = nil
	}
	for _, h := range e.handles {
		h.Close()
	}
	e.handles = nil
	e.interfaces = nil
	e.bpfFilter = ""
	e.targetIPs = nil
	e.targetPorts = nil
}

func (e *Engine) readLoop(ctx context.Context, h captureHandle) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		data, err := h.readRaw()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			e.log.Info("DBG read_err", "iface", h.ifaceName(), "err", err)
			continue
		}
		if len(data) == 0 {
			continue
		}
		n := uint64(0)
		if e.stats != nil {
			n = e.stats.FramesRead.Add(1)
		}
		// #region debug
		if n <= 5 || n%500 == 0 {
			e.log.Info("DBG frame_read", "n", n, "len", len(data), "iface", h.ifaceName(), "hdr4", fmt.Sprintf("%x", firstN(data, 4)))
		}
		// #endregion
		packet := h.decode(data)
		if packet == nil {
			// #region debug
			if n <= 5 {
				e.log.Info("DBG frame_decode_nil", "n", n, "len", len(data), "hdr16", fmt.Sprintf("%x", firstN(data, 16)))
			}
			// #endregion
			continue
		}
		if e.stats != nil {
			e.stats.FramesTCP.Add(1)
		}
		e.mu.Lock()
		ips := e.targetIPs
		ports := e.targetPorts
		e.mu.Unlock()
		if !MatchTargets(packet, ips, ports) {
			if e.stats != nil {
				e.stats.FramesUnmatched.Add(1)
			}
			continue
		}
		if e.stats != nil {
			e.stats.Packets.Add(1)
		}
		e.log.Info("DBG packet_matched", "iface", h.ifaceName())
		e.handler.HandlePacket(packet)
	}
}

func firstN(b []byte, n int) []byte {
	if len(b) < n {
		return b
	}
	return b[:n]
}

// ErrReloadInProgress is returned when config swap is already running.
var ErrReloadInProgress = errors.New("reload already in progress")

// AssemblerHandler wraps tcpassembly for the engine.
type AssemblerHandler struct {
	assembler *tcpassembly.Assembler
}

func NewAssemblerHandler(factory tcpassembly.StreamFactory, pool *tcpassembly.StreamPool) *AssemblerHandler {
	return &AssemblerHandler{
		assembler: tcpassembly.NewAssembler(pool),
	}
}

func (a *AssemblerHandler) HandlePacket(packet gopacket.Packet) {
	netLayer := packet.NetworkLayer()
	tcpLayer := packet.Layer(layers.LayerTypeTCP)
	if netLayer == nil || tcpLayer == nil {
		return
	}
	tcp, ok := tcpLayer.(*layers.TCP)
	if !ok {
		return
	}
	ts := packet.Metadata().Timestamp
	if ts.IsZero() {
		ts = time.Now()
	}
	a.assembler.AssembleWithTimestamp(netLayer.NetworkFlow(), tcp, ts)
}

func (a *AssemblerHandler) FlushOlderThan(t time.Time) {
	_, _ = a.assembler.FlushOlderThan(t)
}

func (a *AssemblerHandler) FlushAll() {
	a.assembler.FlushAll()
}
