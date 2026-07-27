//go:build linux && integration

package capture_test

import (
	"os/exec"
	"testing"
	"time"

	"github.com/shadow-diff/sample"
)

// End-to-end sampling through the real pipeline: real traffic over a veth pair,
// the kernel trace gate, the perf ring, TCP reassembly and HTTP parsing.
//
// The gate tests in package capture drive the BPF program directly, which
// proves the program. This proves the system: that a request the kernel keeps
// survives every stage after it, and that a request the kernel drops never
// reappears. A gate that agreed with pkg/sample but broke reassembly would pass
// those and fail this.
func TestSamplingEndToEnd(t *testing.T) {
	setupLab(t)
	startServer(t, nsA, podAIP, appPort)

	const pct = 10
	// Straddling the 10% boundary: V=0 and V=25 keep, V=26 and V=255 drop.
	ids := []string{
		"00000000000000000000000000000087", // V=0    keep
		"00000000000000000000000000000084", // V=25   keep
		"000000000000000000000000000000f9", // V=26   drop
		"00000000000000000000000000000045", // drop
	}

	var want int
	for _, id := range ids {
		if sample.SampledIn(id, pct) {
			want++
		}
	}
	if want == 0 || want == len(ids) {
		t.Fatalf("test vectors must straddle the boundary; %d of %d keep", want, len(ids))
	}

	c := startCaptureSampled(t, vethA, []string{podAIP}, []uint16{appPort}, pct)

	for i, id := range ids {
		curlTraced(t, nsB, appURL(podAIP, appPort, pathFor(i)), id)
	}

	c.waitFor(want, 10*time.Second)

	// Settle before concluding nothing more is coming, so a gate that merely
	// delayed the dropped requests would still fail here.
	time.Sleep(settleWindow)
	got, _ := c.snapshot()

	if len(got) != want {
		t.Fatalf("captured %d requests, want %d (kernel gate and pkg/sample disagree)", len(got), want)
	}
	for _, req := range got {
		tp := req.Header.Get("traceparent")
		id, ok := sample.TraceIDFromTraceparent(tp)
		if !ok {
			t.Errorf("captured request has no usable traceparent: %q", tp)
			continue
		}
		if !sample.SampledIn(id, pct) {
			t.Errorf("trace id %s reached user space but pkg/sample would drop it", id)
		}
	}
}

func pathFor(i int) string {
	return "/sampled/" + string(rune('a'+i))
}

func curlTraced(t *testing.T, ns, url, traceID string) {
	t.Helper()
	cmd := exec.Command("ip", "netns", "exec", ns, "curl", "-s", "--max-time", "10",
		"-H", "traceparent: 00-"+traceID+"-00f067aa0ba902b7-01",
		"-o", "/dev/null", url)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("curl %s: %v: %s", url, err, out)
	}
}
