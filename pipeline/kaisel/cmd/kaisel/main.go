// Command kaisel captures HTTP traffic with eBPF and prints the reassembled
// requests.
//
// Step 1 prototype: proves the packet path from an AF_PACKET socket filter
// through perf-array transport, TCP reassembly and HTTP parsing. Rule-driven
// capture and OTLP export come later.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"github.com/shadow-diff/kaisel/internal/capture"
	"github.com/shadow-diff/kaisel/internal/decode"
)

// targets collects repeatable -target flags.
type targets []net.IP

func (t *targets) String() string { return "" }

func (t *targets) Set(v string) error {
	for _, s := range strings.Split(v, ",") {
		ip := net.ParseIP(strings.TrimSpace(s))
		if ip == nil || ip.To4() == nil {
			return &net.ParseError{Type: "IPv4 address", Text: s}
		}
		*t = append(*t, ip)
	}
	return nil
}

// ports collects repeatable -port flags.
type ports []uint16

func (p *ports) String() string { return "" }

func (p *ports) Set(v string) error {
	for _, s := range strings.Split(v, ",") {
		n, err := strconv.ParseUint(strings.TrimSpace(s), 10, 16)
		if err != nil || n == 0 {
			return fmt.Errorf("invalid TCP port %q", s)
		}
		*p = append(*p, uint16(n))
	}
	return nil
}

func main() {
	var tgts targets
	var prts ports
	iface := flag.String("iface", "lo", "interface to capture on")
	flag.Var(&tgts, "target", "IPv4 address to capture (repeatable, comma-separated)")
	flag.Var(&prts, "port", "TCP port to capture (repeatable, comma-separated; empty = all ports)")
	l2Off := flag.Int("l2-off", -1, "link-layer header size: -1 autodetect, 0 raw L3, 14 Ethernet")
	perCPU := flag.Int("percpu-buffer", 0, "per-CPU perf ring bytes (0 = default)")
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	if len(tgts) == 0 {
		tgts = targets{net.IPv4(127, 0, 0, 1)}
	}

	framing, err := resolveFraming(*iface, *l2Off)
	if err != nil {
		log.Error("resolve framing", "err", err)
		os.Exit(1)
	}

	ctx, stop := context.WithCancel(context.Background())
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-ch
		log.Info("shutting down")
		stop()
	}()

	if err := capture.Run(ctx, capture.Config{
		Iface:        *iface,
		Targets:      tgts,
		Ports:        prts,
		Framing:      framing,
		PerCPUBuffer: *perCPU,
		Log:          log,
	}); err != nil {
		log.Error("capture", "err", err)
		os.Exit(1)
	}
}

// resolveFraming honours an explicit -l2-off, else autodetects from sysfs.
//
// The override earns its keep: a wrong l2_off makes the kernel's version-nibble
// check fail on every packet, so the symptom is total silence -- indistinguishable
// from "no traffic" without a way to force the other value.
func resolveFraming(iface string, l2Off int) (decode.Framing, error) {
	switch l2Off {
	case -1:
		return capture.DetectFraming(iface)
	case 0:
		return decode.FramingRawIP, nil
	case 14:
		return decode.FramingEthernet, nil
	default:
		return 0, &net.AddrError{Err: "l2-off must be -1, 0 or 14", Addr: "l2-off"}
	}
}
