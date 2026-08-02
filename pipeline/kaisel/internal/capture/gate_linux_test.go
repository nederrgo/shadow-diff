//go:build linux && integration

package capture

import (
	"encoding/binary"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/cilium/ebpf"
	"github.com/cilium/ebpf/perf"
	"github.com/cilium/ebpf/rlimit"
)

// The kernel trace gate, driven through BPF_PROG_TEST_RUN. No network lab is
// needed: the program is fed synthetic frames directly.
//
// capture() returns 0 on every path -- the perf ring is the only consumer, so
// the return value carries no verdict (see the ponytail note at the end of
// capture.c). Pass and drop are therefore asserted on the side effects that do
// distinguish them: the sample_drops counter, which only the gate increments,
// and the admitted LRU, which only a gated request head writes.

const (
	// Golden vectors, identical to pipeline/pkg/sample/sample_test.go. Both
	// halves of the sampling decision must bucket these the same way or the
	// kernel is dropping traffic user space would keep.
	goldenKeepLow  = "00000000000000000000000000000087" // V=0, kept at 10%
	goldenKeepHigh = "00000000000000000000000000000084" // V=25, boundary keep
	goldenDropLow  = "000000000000000000000000000000f9" // V=26, first drop
	goldenDropUp   = "0000000000000000000000000000ABCD" // uppercase, drops

	gateSrcIP = "10.99.0.2"
	gateDstIP = "10.99.0.3"
	gatePct   = 10
)

type gate struct {
	objs bpfObjects
	perf *perf.Reader
	t    *testing.T
}

func newGate(t *testing.T, pct uint8) *gate {
	t.Helper()
	if err := rlimit.RemoveMemlock(); err != nil {
		t.Fatalf("remove memlock: %v", err)
	}
	spec, err := loadBpf()
	if err != nil {
		t.Fatalf("load spec: %v", err)
	}
	// prog_test_run leaves skb->ifindex at 0, and lo_ifindex defaults to 0 --
	// which would make every frame look like loopback and return before the
	// gate. Point it at an ifindex nothing will ever have.
	if err := spec.Variables["lo_ifindex"].Set(uint32(0xffff)); err != nil {
		t.Fatalf("set lo_ifindex: %v", err)
	}
	// bpf_prog_test_run_skb runs the frame through eth_type_trans, which pulls
	// the link-layer header before the program sees the skb. The frames below
	// still carry an Ethernet header -- eth_type_trans needs one -- but by the
	// time capture() runs, offset 0 is the IP header. So this is raw-L3 framing
	// regardless of what the wire looks like.
	if err := spec.Variables["l2_off"].Set(uint32(0)); err != nil {
		t.Fatalf("set l2_off: %v", err)
	}
	spec.Maps["admitted"].MaxEntries = 1024

	g := &gate{t: t}
	if err := spec.LoadAndAssign(&g.objs, nil); err != nil {
		t.Fatalf("load objects: %v", err)
	}
	t.Cleanup(func() { g.objs.Close() })

	g.perf, err = perf.NewReader(g.objs.Events, 4096)
	if err != nil {
		t.Fatalf("open perf reader: %v", err)
	}
	t.Cleanup(func() { g.perf.Close() })

	for _, ip := range []string{gateSrcIP, gateDstIP} {
		key, ok := ipKey(parseIP(t, ip))
		if !ok {
			t.Fatalf("bad target %s", ip)
		}
		if err := g.objs.TargetIps.Put(key, pct); err != nil {
			t.Fatalf("seed target: %v", err)
		}
	}
	return g
}

// drops reads the summed per-CPU gate drop counter.
func (g *gate) drops() uint64 {
	g.t.Helper()
	var perCPU []uint64
	if err := g.objs.SampleDrops.Lookup(uint32(0), &perCPU); err != nil {
		g.t.Fatalf("read sample_drops: %v", err)
	}
	var total uint64
	for _, v := range perCPU {
		total += v
	}
	return total
}

// admitted reports whether the canonical 5-tuple is in the LRU.
func (g *gate) admitted(sport, dport uint16) bool {
	g.t.Helper()
	var v uint8
	err := g.objs.Admitted.Lookup(flowKey(g.t, gateSrcIP, gateDstIP, sport, dport), &v)
	return err == nil
}

// run feeds one frame through the program and reports whether it was dropped.
//
// The verdict comes from the perf ring, not from the return value: capture()
// returns 0 on every path, so "did this reach user space" is only answerable by
// asking whether a record was emitted. That is also the property the daemon
// actually cares about.
func (g *gate) run(pkt []byte) (dropped bool) {
	g.t.Helper()
	ret, err := g.objs.Capture.Run(&ebpf.RunOptions{Data: pkt})
	if err != nil {
		g.t.Fatalf("prog test run: %v", err)
	}
	if ret != 0 {
		g.t.Fatalf("capture returned %d, want 0 on every path", ret)
	}
	return !g.emitted()
}

// emitted drains the perf ring and reports whether anything was there. The
// deadline is what makes "nothing arrived" an answer rather than a hang.
func (g *gate) emitted() bool {
	g.t.Helper()
	g.perf.SetDeadline(time.Now().Add(50 * time.Millisecond))
	_, err := g.perf.Read()
	if err == nil {
		return true
	}
	if errors.Is(err, os.ErrDeadlineExceeded) {
		return false
	}
	g.t.Fatalf("read perf ring: %v", err)
	return false
}

// flowKey mirrors flow_of() in capture.c: endpoints ordered so both directions
// of one connection produce a single key.
func flowKey(t *testing.T, srcIP, dstIP string, sport, dport uint16) []byte {
	t.Helper()
	s, _ := ipKey(parseIP(t, srcIP))
	d, _ := ipKey(parseIP(t, dstIP))
	lo, hi, lop, hip := s, d, sport, dport
	if !(s < d || (s == d && sport <= dport)) {
		lo, hi, lop, hip = d, s, dport, sport
	}
	k := make([]byte, 12)
	binary.LittleEndian.PutUint32(k[0:], lo)
	binary.LittleEndian.PutUint32(k[4:], hi)
	binary.LittleEndian.PutUint16(k[8:], lop)
	binary.LittleEndian.PutUint16(k[10:], hip)
	return k
}
