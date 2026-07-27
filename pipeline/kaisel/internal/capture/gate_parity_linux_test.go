//go:build linux && integration

package capture

import (
	"fmt"
	"testing"

	"github.com/shadow-diff/sample"
)

// The kernel gate and pkg/sample must bucket every trace id identically.
//
// This is the check that actually protects the invariant. The golden vectors
// elsewhere pin four ids; this walks a few hundred and fails on the first
// disagreement, because a divergence in either direction is a silent bug: the
// kernel dropping what user space would keep loses production traffic, and the
// kernel keeping what user space drops just wastes ring bandwidth.
//
// Only ids the kernel actually reaches count -- the gate has to see a request
// head, in-window, to have an opinion at all. Everything else fails open by
// design and is not a parity question.
func TestGateAgreesWithPkgSample(t *testing.T) {
	const pct = 37 // deliberately not a round number

	g := newGate(t, pct)
	var checked int

	for i := 0; i < 256; i++ {
		// Vary the low byte, which is what FNV-1a folds into the bucket.
		traceID := fmt.Sprintf("%030x%02x", 0, i)
		sport := uint16(50000 + i)

		kernelDropped := g.run(req(t, sport, httpHead("GET", traceID, 0)))
		userKeeps := sample.SampledIn(traceID, pct)

		if kernelDropped == userKeeps {
			verdict := map[bool]string{true: "dropped", false: "passed"}
			t.Fatalf("trace id %s at %d%%: kernel %s, pkg/sample says keep=%v",
				traceID, pct, verdict[kernelDropped], userKeeps)
		}
		checked++
	}

	if checked != 256 {
		t.Fatalf("checked %d ids, want 256", checked)
	}
}
