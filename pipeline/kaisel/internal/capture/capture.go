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
// Sampling cannot protect this buffer: the sampling decision needs the
// traceparent, which lives in the payload, so 100% of matched traffic must
// cross into user space before it can be sampled. The ring therefore has to
// absorb peak *unsampled* throughput for the targeted ports, and the only
// levers that reduce what arrives are the kernel-side address, protocol and
// port filters.
const defaultPerCPUPages = 256

// MapUpdate carries incremental changes to the live eBPF address and port maps.
type MapUpdate struct {
	AddIPs      []net.IP
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
	Targets []net.IP
	// Ports restricts capture to these TCP ports, matched against source and
	// destination so both directions land. Empty captures every TCP port.
	Ports []uint16
	// Framing is the link-layer framing. Pushed into the BPF program's l2_off
	// constant and used to decode, so both halves agree by construction.
	Framing decode.Framing
	// PerCPUBuffer is the per-CPU perf ring size in bytes. Zero picks a default.
	PerCPUBuffer int
	Log          *slog.Logger
	// OnRequest receives every parsed HTTP request. Nil logs instead.
	OnRequest func(netFlow, transportFlow gopacket.Flow, req *http.Request)
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

	// Two-step load, not loadBpfObjects: l2_off is a .rodata constant and must
	// be set on the spec before the program reaches the verifier.
	spec, err := loadBpf()
	if err != nil {
		return fmt.Errorf("load spec: %w", err)
	}
	if err := spec.Variables["l2_off"].Set(cfg.Framing.Offset()); err != nil {
		return fmt.Errorf("set l2_off: %w", err)
	}
	// BPF cannot test a map for emptiness, so "filter by port at all" is a
	// constant the verifier folds away when unset.
	portFilterOn := uint32(0)
	if len(cfg.Ports) > 0 {
		portFilterOn = 1
	}
	if err := spec.Variables["port_filter_on"].Set(portFilterOn); err != nil {
		return fmt.Errorf("set port_filter_on: %w", err)
	}
	// Loopback's header layout differs from every other device multiplexed
	// onto an ifindex-0 socket, so it is excluded by ifindex rather than
	// misread with the wrong l2_off. Harmless when bound to a single named
	// interface: that interface's ifindex is never lo's.
	loIfindex := uint32(0)
	if lo, err := net.InterfaceByName("lo"); err == nil {
		loIfindex = uint32(lo.Index)
	}
	if err := spec.Variables["lo_ifindex"].Set(loIfindex); err != nil {
		return fmt.Errorf("set lo_ifindex: %w", err)
	}

	var objs bpfObjects
	if err := spec.LoadAndAssign(&objs, nil); err != nil {
		return fmt.Errorf("load objects: %w", err)
	}
	defer objs.Close()

	for _, ip := range cfg.Targets {
		key, ok := ipKey(ip)
		if !ok {
			return fmt.Errorf("target %s is not IPv4", ip)
		}
		if err := objs.TargetIps.Put(key, uint8(1)); err != nil {
			return fmt.Errorf("seed target %s: %w", ip, err)
		}
	}
	for _, port := range cfg.Ports {
		if err := objs.TargetPorts.Put(port, uint8(1)); err != nil {
			return fmt.Errorf("seed port %d: %w", port, err)
		}
	}

	sock, err := openRawSocket(ifIndex, objs.Capture.FD())
	if err != nil {
		return err
	}
	defer unix.Close(sock)

	perCPU := cfg.PerCPUBuffer
	if perCPU <= 0 {
		perCPU = os.Getpagesize() * defaultPerCPUPages
	}
	src, err := newPerfSource(objs.Events, perCPU)
	if err != nil {
		return err
	}
	defer src.Close()

	log.Info("kaisel capturing",
		"iface", cfg.Iface, "framing", cfg.Framing.String(),
		"targets", len(cfg.Targets), "ports", cfg.Ports,
		"per_cpu_buffer", perCPU)

	// Closing the source is what unblocks Read on shutdown.
	go func() {
		<-ctx.Done()
		src.Close()
	}()

	if cfg.Ready != nil {
		cfg.Ready()
	}

	return consume(ctx, src, cfg, log, src.stats, fragDrops(objs.FragDrops), &objs, portFilterOn)
}

// fragDrops sums the per-CPU fragment drop counter. A failed lookup reports 0
// rather than an error: this is diagnostic, and it must never take down a
// running capture.
func fragDrops(m *ebpf.Map) func() uint64 {
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
func applyUpdate(objs *bpfObjects, upd MapUpdate, portFilterOn uint32, log *slog.Logger) {
	if portFilterOn == 0 && len(upd.AddPorts) > 0 {
		log.Warn("port_filter_on is 0; added ports have no effect until daemon restarts with -port flags")
	}
	for _, ip := range upd.AddIPs {
		if key, ok := ipKey(ip); ok {
			if err := objs.TargetIps.Put(key, uint8(1)); err != nil {
				log.Warn("add IP to map", "ip", ip, "err", err)
			}
		}
	}
	for _, ip := range upd.RemoveIPs {
		if key, ok := ipKey(ip); ok {
			if err := objs.TargetIps.Delete(key); err != nil {
				log.Warn("remove IP from map", "ip", ip, "err", err)
			}
		}
	}
	for _, p := range upd.AddPorts {
		if err := objs.TargetPorts.Put(p, uint8(1)); err != nil {
			log.Warn("add port to map", "port", p, "err", err)
		}
	}
	for _, p := range upd.RemovePorts {
		if err := objs.TargetPorts.Delete(p); err != nil {
			log.Warn("remove port from map", "port", p, "err", err)
		}
	}
}

// consume drives the read loop: source -> decode -> reassembly.
func consume(ctx context.Context, src PacketSource, cfg Config, log *slog.Logger, stats func() (uint64, uint64), frags func() uint64, objs *bpfObjects, portFilterOn uint32) error {
	factory := &decode.StreamFactory{Log: log, OnRequest: cfg.OnRequest, LogBodies: cfg.LogBodies}
	asm := tcpassembly.NewAssembler(tcpassembly.NewStreamPool(factory))

	ticker := time.NewTicker(flushInterval)
	defer ticker.Stop()
	done := make(chan struct{})
	defer close(done)
	go func() {
		var lastFrags uint64
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				asm.FlushOlderThan(time.Now().Add(-flushAge))
				// A dropped fragment is a request we will never report. Rare
				// enough to be worth a warning every time it happens.
				if n := frags(); n > lastFrags {
					log.Warn("dropped fragmented IP datagrams; those requests are not captured",
						"total", n, "since_last", n-lastFrags)
					lastFrags = n
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
