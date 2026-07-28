//go:build linux && integration

package capture

import (
	"errors"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/cilium/ebpf"
	"github.com/cilium/ebpf/perf"
	"github.com/cilium/ebpf/rlimit"

	"github.com/shadow-diff/kaisel/internal/decode"
)

// The ungated build -- the one a kernel without bpf_loop gets -- and the tier
// selection that picks it.
//
// Neither can be exercised naturally here: this box runs 6.18 and the minikube
// used for cluster E2E runs 6.6, so the gated object always loads. These tests
// are the only thing standing between a broken #ifdef and a fleet of nodes
// capturing nothing.

// ungated is the nogate equivalent of the gate harness in gate_linux_test.go.
// Separate rather than shared: bpf2go names its types after the output prefix,
// so nogateObjects and bpfObjects are unrelated Go types.
type ungated struct {
	objs nogateObjects
	perf *perf.Reader
	t    *testing.T
}

func newUngated(t *testing.T, pct uint8) *ungated {
	t.Helper()
	if err := rlimit.RemoveMemlock(); err != nil {
		t.Fatalf("remove memlock: %v", err)
	}
	spec, err := loadNogate()
	if err != nil {
		t.Fatalf("load spec: %v", err)
	}
	// Same two adjustments the gated harness makes: prog_test_run leaves
	// ifindex at 0, and eth_type_trans has already pulled the L2 header by the
	// time capture() sees the skb.
	if err := spec.Variables["lo_ifindex"].Set(uint32(0xffff)); err != nil {
		t.Fatalf("set lo_ifindex: %v", err)
	}
	if err := spec.Variables["l2_off"].Set(uint32(0)); err != nil {
		t.Fatalf("set l2_off: %v", err)
	}

	u := &ungated{t: t}
	if err := spec.LoadAndAssign(&u.objs, nil); err != nil {
		t.Fatalf("load objects: %v", err)
	}
	t.Cleanup(func() { u.objs.Close() })

	u.perf, err = perf.NewReader(u.objs.Events, 4096)
	if err != nil {
		t.Fatalf("open perf reader: %v", err)
	}
	t.Cleanup(func() { u.perf.Close() })

	for _, ip := range []string{gateSrcIP, gateDstIP} {
		key, ok := ipKey(parseIP(t, ip))
		if !ok {
			t.Fatalf("bad target %s", ip)
		}
		if err := u.objs.TargetIps.Put(key, pct); err != nil {
			t.Fatalf("seed target: %v", err)
		}
	}
	return u
}

func (u *ungated) run(pkt []byte) (dropped bool) {
	u.t.Helper()
	ret, err := u.objs.Capture.Run(&ebpf.RunOptions{Data: pkt})
	if err != nil {
		u.t.Fatalf("prog test run: %v", err)
	}
	if ret != 0 {
		u.t.Fatalf("capture returned %d, want 0 on every path", ret)
	}
	u.perf.SetDeadline(time.Now().Add(50 * time.Millisecond))
	if _, err := u.perf.Read(); err == nil {
		return false
	} else if !errors.Is(err, os.ErrDeadlineExceeded) {
		u.t.Fatalf("read perf ring: %v", err)
	}
	return true
}

// The ungated object has to verify, and it has to carry exactly the maps the
// #ifdefs were meant to leave it. This is what breaks first if KAISEL_GATE
// stops lining up.
func TestNoGateObjectLoads(t *testing.T) {
	spec, err := loadNogate()
	if err != nil {
		t.Fatal(err)
	}
	var objs nogateObjects
	if err := spec.LoadAndAssign(&objs, nil); err != nil {
		t.Fatalf("ungated object rejected by the verifier: %v", err)
	}
	defer objs.Close()

	for _, m := range []string{"hdr_buf", "admitted", "sample_drops"} {
		if _, ok := spec.Maps[m]; ok {
			t.Errorf("ungated build still carries the gate-only map %q", m)
		}
	}
	for _, m := range []string{"target_ips", "target_ports", "frag_drops", "events"} {
		if _, ok := spec.Maps[m]; !ok {
			t.Errorf("ungated build lost %q, which is not gate machinery", m)
		}
	}
}

// A trace id the gate buckets out at 10% must still reach user space here --
// that is the whole difference between the tiers. If this drops, the ungated
// build is gating anyway and Tier 2 nodes would silently under-sample.
func TestNoGatePassesEverything(t *testing.T) {
	u := newUngated(t, gatePct)
	for _, id := range []string{goldenDropLow, goldenDropUp, goldenKeepLow} {
		pkt := seg{
			srcIP: gateSrcIP, dstIP: gateDstIP, sport: 40001, dport: 8080,
			flags: tcpACK, payload: httpHead("GET", id, 0),
		}.build(t)
		if u.run(pkt) {
			t.Errorf("trace id %s dropped; the ungated build must not gate", id)
		}
	}
	// No traceparent at all is the same story from the other side.
	pkt := seg{
		srcIP: gateSrcIP, dstIP: gateDstIP, sport: 40002, dport: 8080,
		flags: tcpACK, payload: httpHead("GET", "", 0),
	}.build(t)
	if u.run(pkt) {
		t.Error("untraced request dropped by the ungated build")
	}
}

// The #ifdefs had to take the gate out without taking the flow filters with
// it. These are the drops that must survive.
func TestNoGateKeepsFlowFilters(t *testing.T) {
	u := newUngated(t, gatePct)

	tests := []struct {
		name string
		pkt  seg
	}{{
		// Payload-free and not SYN/FIN/RST: nothing to reassemble.
		name: "pure ACK",
		pkt: seg{srcIP: gateSrcIP, dstIP: gateDstIP, sport: 41001, dport: 8080,
			flags: tcpACK},
	}, {
		// Neither endpoint is in target_ips.
		name: "non-target addresses",
		pkt: seg{srcIP: "192.0.2.10", dstIP: "192.0.2.11", sport: 41002, dport: 8080,
			flags: tcpACK, payload: httpHead("GET", goldenKeepLow, 0)},
	}, {
		// Data offset below the 20-byte TCP minimum.
		name: "malformed data offset",
		pkt: seg{srcIP: gateSrcIP, dstIP: gateDstIP, sport: 41003, dport: 8080,
			flags: tcpACK, rawDoff: 3, payload: httpHead("GET", goldenKeepLow, 0)},
	}}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if !u.run(tc.pkt.build(t)) {
				t.Errorf("%s reached user space; the ungated build dropped a flow filter", tc.name)
			}
		})
	}

	// And a frame that should pass still does, so the cases above are proving
	// the filters rather than a program that drops everything.
	pkt := seg{srcIP: gateSrcIP, dstIP: gateDstIP, sport: 41004, dport: 8080,
		flags: tcpACK, payload: httpHead("GET", goldenKeepLow, 0)}.build(t)
	if u.run(pkt) {
		t.Error("a matching frame was dropped; the flow filters are over-tight")
	}
}

// forceTierFailure makes the candidate at idx fail to load, restoring the real
// one when the test ends. This is the only way to reach the fallback branch on
// a kernel that runs the gate perfectly well.
func forceTierFailure(t *testing.T, idx int) {
	t.Helper()
	orig := tierCandidates[idx].load
	tierCandidates[idx].load = func() (*ebpf.CollectionSpec, error) {
		return nil, ebpf.ErrNotSupported
	}
	t.Cleanup(func() { tierCandidates[idx].load = orig })
}

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// The control for the fallback test below: unforced, this kernel (>= 5.17)
// must take the gate. Without this, TestFallsBackWhenGateRejected would still
// pass if the candidate order were reversed and every node quietly ran ungated.
func TestPrefersGateWhenAvailable(t *testing.T) {
	if err := rlimit.RemoveMemlock(); err != nil {
		t.Fatalf("remove memlock: %v", err)
	}
	objs, err := loadProgram(Config{Framing: decode.FramingEthernet}, 0xffff, 0, quietLogger())
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	defer objs.close()

	if objs.tier != TierBPFLoop {
		t.Fatalf("tier = %d on kernel %s, want %d (bpf_loop)",
			objs.tier, kernelRelease(), TierBPFLoop)
	}
	if objs.sampleDrops == nil {
		t.Error("sampleDrops is nil at TierBPFLoop; the gate cannot report its drop rate")
	}
}

// The branch a pre-5.17 node takes: gated object rejected, ungated one loads,
// capture continues with sampling in user space.
func TestFallsBackWhenGateRejected(t *testing.T) {
	if err := rlimit.RemoveMemlock(); err != nil {
		t.Fatalf("remove memlock: %v", err)
	}
	forceTierFailure(t, 0)

	objs, err := loadProgram(Config{Framing: decode.FramingEthernet}, 0xffff, 0, quietLogger())
	if err != nil {
		t.Fatalf("no tier loaded after the gate was refused: %v", err)
	}
	defer objs.close()

	if objs.tier != TierUser {
		t.Errorf("tier = %d, want %d (userspace)", objs.tier, TierUser)
	}
	// The flush ticker reads this unconditionally, so a nil that is not
	// tolerated would panic a running daemon rather than fail a test.
	if objs.sampleDrops != nil {
		t.Error("sampleDrops is non-nil at TierUser; the ungated build has no such map")
	}
	if got := perCPUCounter(objs.sampleDrops)(); got != 0 {
		t.Errorf("perCPUCounter(nil) = %d, want 0", got)
	}
	// Everything Run() actually drives has to be there regardless of tier.
	if objs.prog == nil || objs.targetIPs == nil || objs.targetPorts == nil ||
		objs.events == nil || objs.fragDrops == nil {
		t.Error("TierUser is missing a map or program that Run() dereferences")
	}
}

// Below 5.2 nothing loads. The daemon must say which kernel it is on and what
// it needs, not surface a raw verifier rejection naming an instruction.
func TestRefusesWhenNoTierLoads(t *testing.T) {
	for i := range tierCandidates {
		forceTierFailure(t, i)
	}

	_, err := loadProgram(Config{Framing: decode.FramingEthernet}, 0xffff, 0, quietLogger())
	if err == nil {
		t.Fatal("loadProgram succeeded with every tier failing")
	}
	msg := err.Error()
	for _, want := range []string{"5.2", kernelRelease()} {
		if !strings.Contains(msg, want) {
			t.Errorf("error does not mention %q: %s", want, msg)
		}
	}
}
