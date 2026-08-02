package capture

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/cilium/ebpf"
	"github.com/cilium/ebpf/asm"
	"github.com/cilium/ebpf/features"
	"github.com/cilium/ebpf/rlimit"
	"github.com/gopacket/gopacket"
	"github.com/gopacket/gopacket/layers"
	"github.com/gopacket/gopacket/tcpassembly"
	"golang.org/x/sys/unix"

	"github.com/shadow-diff/kaisel/internal/decode"
)

// flushInterval and flushAge control how long a half-open stream is held before
// the assembler gives up on the missing segments.
const (
	flushInterval = 30 * time.Second
	flushAge      = 2 * time.Minute
)

// defaultPerCPUPages sizes the perf ring at 1MB per CPU.
//
// The kernel trace gate reduces what has to fit here to roughly the sampled
// share of matched traffic, plus whatever fails open (untraced requests, and
// heads whose traceparent falls outside the scan window). The ring still has
// to absorb bursts of that, since the gate decides per request rather than
// smoothing rate.
const defaultPerCPUPages = 256

// defaultAdmittedEntries bounds the kernel LRU of sampled-in connections.
// Roughly 2-3MB of locked memory. Sized for concurrent tracked flows per node,
// not requests: one entry covers a keep-alive connection for its lifetime.
const defaultAdmittedEntries = 32768

// TargetIP is one capture target and the sample percentage its KaiselRule
// carries. The percentage reaches the kernel because the trace gate lives
// there; it stays per-IP because samplePercentage is per-ShadowTest and so
// cannot be a load-time constant. 0 means unset, which the gate reads as 100.
type TargetIP struct {
	IP               net.IP
	SamplePercentage int
}

// MapUpdate carries incremental changes to the live eBPF address and port maps.
type MapUpdate struct {
	AddIPs      []TargetIP
	RemoveIPs   []net.IP
	AddPorts    []uint16
	RemovePorts []uint16
}

// Config configures a capture run.
type Config struct {
	// Iface is the interface to bind the raw socket to. Binding matters:
	// unbound, the filter would see every interface on the host.
	Iface string
	// Targets are the IPv4 addresses to capture; a frame matches on either
	// source or destination.
	Targets []TargetIP
	// Ports restricts capture to these TCP ports, matched against source and
	// destination so both directions land. Empty captures every TCP port.
	Ports []uint16
	// Framing is the link-layer framing. Pushed into the BPF program's l2_off
	// constant and used to decode, so both halves agree by construction.
	Framing decode.Framing
	// PerCPUBuffer is the per-CPU perf ring size in bytes. Zero picks a default.
	PerCPUBuffer int
	// AdmittedEntries bounds the kernel LRU of sampled-in connections. Zero
	// picks a default. Raise it on nodes dense enough that the coldest flow
	// gets evicted while still active, which costs an ungated request.
	AdmittedEntries int
	Log             *slog.Logger
	// OnRequest receives every parsed HTTP request. Nil logs instead.
	OnRequest func(netFlow, transportFlow gopacket.Flow, req *http.Request)
	// OnTransaction receives a request paired with its response, for egress
	// mock recording. Nil disables response parsing entirely.
	OnTransaction func(netFlow, transportFlow gopacket.Flow, req *http.Request, resp *http.Response)
	// WantTransaction gates response pairing per stream, so connections with
	// no egress route never buffer a response body.
	WantTransaction func(netFlow gopacket.Flow) bool
	// LogBodies includes request body content (truncated) in the default log
	// line when OnRequest is nil. Off by default: captured bodies are real
	// production data, and logs are commonly shipped off-node.
	LogBodies bool
	// Ready, if set, is called once the filter is attached and the perf reader
	// is open. Traffic generated before this fires is not guaranteed to be
	// seen; tests use it instead of sleeping.
	Ready func()
	// Updates, if non-nil, delivers live map changes into the capture loop.
	// ponytail: applied once per incoming packet; quiescent targets see a delay
	// until the next frame arrives. Upgrade: process in the flush-ticker goroutine.
	Updates <-chan MapUpdate
	// OnTier, if set, is called once with the tier the kernel accepted. Exists
	// so main can publish the tier as a metric without this package importing
	// prometheus.
	OnTier func(Tier)
}

// Tier is which build of capture.c this kernel accepted.
//
// The trace gate scans with bpf_loop, which lands in kernel 5.17. An
// unreachable bpf_loop still fails verification -- the verifier checks every
// instruction, not just reachable ones -- so the fallback cannot be a runtime
// flag and has to be a separately compiled object.
type Tier int

const (
	// TierBPFLoop is the gated build: the kernel decides sampling itself.
	TierBPFLoop Tier = 1
	// TierUser is the ungated build. The kernel still runs every flow-level
	// filter; it just does not read payload, so pkg/sample does all the
	// sampling in user space. Results are identical either way -- user space
	// was always authoritative -- at the cost of more traffic across the
	// perf ring.
	TierUser Tier = 2
)

func (t Tier) String() string {
	switch t {
	case TierBPFLoop:
		return "bpf_loop"
	case TierUser:
		return "userspace"
	}
	return "unknown"
}

// loaded is the map and program set Run drives, whichever tier loaded. The two
// builds yield distinct Go types with near-identical fields -- bpf2go names
// them after the output prefix -- and the ungated one has no gate maps at all,
// so everything downstream takes this instead of either concrete type.
type loaded struct {
	tier        Tier
	prog        *ebpf.Program
	targetIPs   *ebpf.Map
	targetPorts *ebpf.Map
	fragDrops   *ebpf.Map
	events      *ebpf.Map
	sampleDrops *ebpf.Map // nil at TierUser: nothing counts gate drops there
	close       func() error
}

// candidate is one build of the program, in descending order of capability.
type candidate struct {
	tier   Tier
	load   func() (*ebpf.CollectionSpec, error)
	assign func(*ebpf.CollectionSpec) (*loaded, error)
}

// tierCandidates is walked in order by loadProgram. A package-level var rather
// than a literal so tests can force a lower tier on a kernel that would happily
// run the gate -- there is no pre-5.17 kernel to test the fallback on
// otherwise.
var tierCandidates = []candidate{
	{TierBPFLoop, loadBpf, assignGated},
	{TierUser, loadNogate, assignUngated},
}

func assignGated(spec *ebpf.CollectionSpec) (*loaded, error) {
	var objs bpfObjects
	if err := spec.LoadAndAssign(&objs, nil); err != nil {
		return nil, err
	}
	return &loaded{
		tier: TierBPFLoop, prog: objs.Capture,
		targetIPs: objs.TargetIps, targetPorts: objs.TargetPorts,
		fragDrops: objs.FragDrops, events: objs.Events,
		sampleDrops: objs.SampleDrops, close: objs.Close,
	}, nil
}

func assignUngated(spec *ebpf.CollectionSpec) (*loaded, error) {
	var objs nogateObjects
	if err := spec.LoadAndAssign(&objs, nil); err != nil {
		return nil, err
	}
	return &loaded{
		tier: TierUser, prog: objs.Capture,
		targetIPs: objs.TargetIps, targetPorts: objs.TargetPorts,
		fragDrops: objs.FragDrops, events: objs.Events,
		close: objs.Close,
	}, nil
}

// prepareSpec applies the load-time constants both builds share.
//
// l2_off, port_filter_on and lo_ifindex are .rodata constants: the verifier
// folds them, so they must be set before the program reaches it. admitted is a
// map property rather than a constant, but has to be fixed before the map is
// created for the same reason -- and it exists only in the gated build, so it
// is set conditionally rather than indexed blind.
func prepareSpec(spec *ebpf.CollectionSpec, cfg Config, loIfindex, portFilterOn uint32) error {
	if err := spec.Variables["l2_off"].Set(cfg.Framing.Offset()); err != nil {
		return fmt.Errorf("set l2_off: %w", err)
	}
	if err := spec.Variables["port_filter_on"].Set(portFilterOn); err != nil {
		return fmt.Errorf("set port_filter_on: %w", err)
	}
	if err := spec.Variables["lo_ifindex"].Set(loIfindex); err != nil {
		return fmt.Errorf("set lo_ifindex: %w", err)
	}
	if m, ok := spec.Maps["admitted"]; ok {
		admitted := cfg.AdmittedEntries
		if admitted <= 0 {
			admitted = defaultAdmittedEntries
		}
		m.MaxEntries = uint32(admitted)
	}
	return nil
}

// loadProgram returns the first build this kernel accepts.
//
// The load attempt is the test, not the kernel version. RHEL 9 ships 5.14 with
// bpf_loop backported, so a version comparison would drop RHCOS to TierUser on
// a kernel that runs the gate fine; trying in order is self-configuring and
// needs no kernel allow-list.
func loadProgram(cfg Config, loIfindex, portFilterOn uint32, log *slog.Logger) (*loaded, error) {
	var errs []error
	for _, c := range tierCandidates {
		spec, err := c.load()
		if err != nil {
			errs = append(errs, fmt.Errorf("tier %d spec: %w", c.tier, err))
			continue
		}
		if err := prepareSpec(spec, cfg, loIfindex, portFilterOn); err != nil {
			errs = append(errs, fmt.Errorf("tier %d: %w", c.tier, err))
			continue
		}
		l, err := c.assign(spec)
		if err != nil {
			errs = append(errs, fmt.Errorf("tier %d load: %w", c.tier, err))
			continue
		}
		if c.tier != TierBPFLoop {
			log.Warn("kaisel trace gate unavailable; sampling runs in user space instead. "+
				"Capture and diff results are unaffected: more matched traffic crosses the "+
				"perf ring before pkg/sample gates it",
				"tier", int(l.tier), "mode", l.tier.String(),
				"reason", gateUnavailableReason(), "kernel", kernelRelease())
		} else {
			log.Info("kaisel trace gate active", "tier", int(l.tier), "mode", l.tier.String())
		}
		return l, nil
	}
	// Below 5.2 nothing loads: three volatile const declarations become frozen
	// read-only maps, which land there. Exiting non-zero is deliberate -- a
	// node capturing zero silently is the failure mode this daemon exists to
	// avoid, so a crashloop that names the reason beats quiet uselessness.
	return nil, fmt.Errorf("no eBPF object this kernel accepts (running %s); "+
		"kaisel needs kernel >= 5.2 for frozen read-only maps: %w",
		kernelRelease(), errors.Join(errs...))
}

// gateUnavailableReason explains why the gated build did not load, for the log
// line only -- never for routing.
//
// The probe is conclusive in exactly two cases: nil, and ErrNotSupported when
// the verifier log names the missing helper. It infers "available" from EACCES
// and otherwise returns an opaque error. It is also a 3-instruction program
// with no maps, so a kernel can pass it and still reject the real one. What it
// buys is separating "this kernel has no bpf_loop" from "your memlock is too
// low", which need opposite responses from whoever reads the log.
func gateUnavailableReason() string {
	switch err := features.HaveProgramHelper(ebpf.SocketFilter, asm.FnLoop); {
	case err == nil:
		return "kernel has bpf_loop but rejected the gated program (check memlock and dmesg)"
	case errors.Is(err, ebpf.ErrNotSupported):
		return "kernel lacks bpf_loop, which needs >= 5.17"
	default:
		return "could not probe bpf_loop: " + err.Error()
	}
}

// kernelRelease reports uname -r, or "unknown" if it cannot be read. Used in
// messages only, so a failure here must not mask the error being reported.
func kernelRelease() string {
	var u unix.Utsname
	if err := unix.Uname(&u); err != nil {
		return "unknown"
	}
	return unix.ByteSliceToString(u.Release[:])
}

// ipKey converts an IPv4 address to a target_ips map key: its plain numeric
// value, 10.99.0.2 to 0x0A630002. capture.c composes the same value byte-wise
// from the header, so the key does not depend on the node's endianness and
// target_ips follows the same host-order rule as target_ports.
func ipKey(ip net.IP) (uint32, bool) {
	v4 := ip.To4()
	if v4 == nil {
		return 0, false
	}
	return binary.BigEndian.Uint32(v4), true
}

// putTarget writes one capture target and its sample percentage into the
// kernel address map. The value is clamped into a uint8: the CRD already
// validates 1-100, but a bad value must not wrap into a percentage that
// silently drops production traffic.
func putTarget(m *ebpf.Map, t TargetIP) error {
	key, ok := ipKey(t.IP)
	if !ok {
		return fmt.Errorf("target %s is not IPv4", t.IP)
	}
	pct := t.SamplePercentage
	if pct < 0 || pct > 100 {
		pct = 100
	}
	if err := m.Put(key, uint8(pct)); err != nil {
		return fmt.Errorf("seed target %s: %w", t.IP, err)
	}
	return nil
}

// Run loads the collector, attaches it, and streams packets until ctx is done.
func Run(ctx context.Context, cfg Config) error {
	log := cfg.Log
	if log == nil {
		log = slog.Default()
	}

	// "any" binds the raw socket to ifindex 0 -- every interface in the
	// socket's network namespace, not just one named device. That's the only
	// way to see same-node pod-to-pod traffic: a Linux bridge never clones
	// frames it merely forwards between two other ports to a listener on any
	// single interface, including the bridge device itself.
	ifIndex := 0
	if cfg.Iface != "any" {
		iface, err := net.InterfaceByName(cfg.Iface)
		if err != nil {
			return fmt.Errorf("lookup interface %q: %w", cfg.Iface, err)
		}
		ifIndex = iface.Index
	}

	if err := rlimit.RemoveMemlock(); err != nil {
		return fmt.Errorf("remove memlock: %w", err)
	}

	// BPF cannot test a map for emptiness, so "filter by port at all" is a
	// constant the verifier folds away when unset.
	portFilterOn := uint32(0)
	if len(cfg.Ports) > 0 {
		portFilterOn = 1
	}
	// Loopback's header layout differs from every other device multiplexed
	// onto an ifindex-0 socket, so it is excluded by ifindex rather than
	// misread with the wrong l2_off. Harmless when bound to a single named
	// interface: that interface's ifindex is never lo's.
	loIfindex := uint32(0)
	if lo, err := net.InterfaceByName("lo"); err == nil {
		loIfindex = uint32(lo.Index)
	}

	objs, err := loadProgram(cfg, loIfindex, portFilterOn, log)
	if err != nil {
		return err
	}
	defer objs.close()

	if cfg.OnTier != nil {
		cfg.OnTier(objs.tier)
	}

	for _, t := range cfg.Targets {
		if err := putTarget(objs.targetIPs, t); err != nil {
			return err
		}
	}
	for _, port := range cfg.Ports {
		if err := objs.targetPorts.Put(port, uint8(1)); err != nil {
			return fmt.Errorf("seed port %d: %w", port, err)
		}
	}

	sock, err := openRawSocket(ifIndex, objs.prog.FD())
	if err != nil {
		return err
	}
	defer unix.Close(sock)

	perCPU := cfg.PerCPUBuffer
	if perCPU <= 0 {
		perCPU = os.Getpagesize() * defaultPerCPUPages
	}
	src, err := newPerfSource(objs.events, perCPU)
	if err != nil {
		return err
	}
	defer src.Close()

	log.Info("kaisel capturing",
		"iface", cfg.Iface, "framing", cfg.Framing.String(),
		"targets", len(cfg.Targets), "ports", cfg.Ports,
		"per_cpu_buffer", perCPU, "tier", int(objs.tier), "mode", objs.tier.String())

	// Closing the source is what unblocks Read on shutdown.
	go func() {
		<-ctx.Done()
		src.Close()
	}()

	if cfg.Ready != nil {
		cfg.Ready()
	}

	return consume(ctx, src, cfg, log, src.stats, counters{
		frags:   perCPUCounter(objs.fragDrops),
		sampled: perCPUCounter(objs.sampleDrops),
	}, objs, portFilterOn)
}

// perCPUCounter sums a BPF_MAP_TYPE_PERCPU_ARRAY counter at index 0. A failed
// lookup reports 0 rather than an error: these are diagnostic, and they must
// never take down a running capture. A nil map reports 0 for the same reason --
// sample_drops does not exist at TierUser, and the flush ticker should not have
// to know which tier it is running under.
func perCPUCounter(m *ebpf.Map) func() uint64 {
	if m == nil {
		return func() uint64 { return 0 }
	}
	return func() uint64 {
		var perCPU []uint64
		if err := m.Lookup(uint32(0), &perCPU); err != nil {
			return 0
		}
		var total uint64
		for _, v := range perCPU {
			total += v
		}
		return total
	}
}

// openRawSocket creates an AF_PACKET socket bound to ifIndex and attaches the
// BPF program to it.
func openRawSocket(ifIndex, progFD int) (int, error) {
	proto := htons(unix.ETH_P_ALL)
	sock, err := unix.Socket(unix.AF_PACKET, unix.SOCK_RAW, int(proto))
	if err != nil {
		return -1, fmt.Errorf("open AF_PACKET socket (needs CAP_NET_RAW): %w", err)
	}
	if err := unix.Bind(sock, &unix.SockaddrLinklayer{
		Protocol: proto,
		Ifindex:  ifIndex,
	}); err != nil {
		unix.Close(sock)
		return -1, fmt.Errorf("bind socket to interface: %w", err)
	}
	if err := unix.SetsockoptInt(sock, unix.SOL_SOCKET, unix.SO_ATTACH_BPF, progFD); err != nil {
		unix.Close(sock)
		return -1, fmt.Errorf("attach BPF filter (needs CAP_BPF): %w", err)
	}
	return sock, nil
}

// applyUpdate mutates the live eBPF hash maps without reloading the program.
func applyUpdate(objs *loaded, upd MapUpdate, portFilterOn uint32, log *slog.Logger) {
	if portFilterOn == 0 && len(upd.AddPorts) > 0 {
		log.Warn("port_filter_on is 0; added ports have no effect until daemon restarts with -port flags")
	}
	for _, t := range upd.AddIPs {
		if err := putTarget(objs.targetIPs, t); err != nil {
			log.Warn("add IP to map", "ip", t.IP, "err", err)
		}
	}
	for _, ip := range upd.RemoveIPs {
		if key, ok := ipKey(ip); ok {
			if err := objs.targetIPs.Delete(key); err != nil {
				log.Warn("remove IP from map", "ip", ip, "err", err)
			}
		}
	}
	for _, p := range upd.AddPorts {
		if err := objs.targetPorts.Put(p, uint8(1)); err != nil {
			log.Warn("add port to map", "port", p, "err", err)
		}
	}
	for _, p := range upd.RemovePorts {
		if err := objs.targetPorts.Delete(p); err != nil {
			log.Warn("remove port from map", "port", p, "err", err)
		}
	}
}

// counters are the kernel-side per-CPU diagnostics the flush ticker reports.
type counters struct {
	frags   func() uint64
	sampled func() uint64
}

// consume drives the read loop: source -> decode -> reassembly.
func consume(ctx context.Context, src PacketSource, cfg Config, log *slog.Logger, stats func() (uint64, uint64), cnt counters, objs *loaded, portFilterOn uint32) error {
	factory := &decode.StreamFactory{
		Log:             log,
		OnRequest:       cfg.OnRequest,
		LogBodies:       cfg.LogBodies,
		OnTransaction:   cfg.OnTransaction,
		WantTransaction: cfg.WantTransaction,
	}
	asm := tcpassembly.NewAssembler(tcpassembly.NewStreamPool(factory))

	ticker := time.NewTicker(flushInterval)
	defer ticker.Stop()
	done := make(chan struct{})
	defer close(done)
	go func() {
		var lastFrags, lastSampled uint64
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				asm.FlushOlderThan(time.Now().Add(-flushAge))
				// Same cadence as the assembler flush: a connection whose
				// response half never arrived leaves pairing state behind.
				factory.Sweep()
				// A dropped fragment is a request we will never report. Rare
				// enough to be worth a warning every time it happens.
				if n := cnt.frags(); n > lastFrags {
					log.Warn("dropped fragmented IP datagrams; those requests are not captured",
						"total", n, "since_last", n-lastFrags)
					lastFrags = n
				}
				// Sampling out is the point, so this is Info rather than Warn
				// -- but a gate that swallows everything looks exactly like a
				// gate that works until someone can see the rate.
				if n := cnt.sampled(); n > lastSampled {
					log.Info("kernel trace gate dropped packets",
						"total", n, "since_last", n-lastSampled)
					lastSampled = n
				}
			case upd, ok := <-cfg.Updates:
				// Drained here, not in the packet-read loop below: target_ips
				// starts empty, so no packet can pass the kernel filter until an
				// update arrives. Draining only after src.Read() would deadlock
				// on startup — nothing to read until the update that unblocks
				// reading is itself read.
				if ok {
					applyUpdate(objs, upd, portFilterOn, log)
				}
			}
		}
	}()

	var ev PacketEvent
	for {
		if err := src.Read(&ev); err != nil {
			if errors.Is(err, ErrClosed) || ctx.Err() != nil {
				log.Info("kaisel stopped")
				return nil
			}
			return fmt.Errorf("read packet: %w", err)
		}

		// Silent loss is the most expensive failure mode in a capture tool.
		if ev.Lost > 0 {
			discarded, oversized := stats()
			log.Warn("kernel dropped records; consumer is behind or ring is undersized",
				"lost", ev.Lost, "packets_discarded", discarded, "oversized", oversized)
		}
		// A short body must never be mistaken for a complete one: replayed into
		// the shadow stack it yields a green result that tested nothing.
		if ev.Truncated {
			log.Warn("packet truncated; body is incomplete",
				"wire_len", ev.OrigLen, "captured", len(ev.Data))
		}
		if len(ev.Data) == 0 {
			continue
		}

		p, ok := decode.Packet(ev.Data, cfg.Framing)
		if !ok {
			continue
		}
		tcp, isTCP := p.TransportLayer().(*layers.TCP)
		if !isTCP {
			continue
		}
		asm.AssembleWithTimestamp(p.NetworkLayer().NetworkFlow(), tcp, time.Now())
	}
}

// htons converts a uint16 to network byte order.
func htons(v uint16) uint16 { return v<<8 | v>>8 }
