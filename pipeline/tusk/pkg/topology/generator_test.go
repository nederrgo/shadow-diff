package topology

import (
	"testing"

	"github.com/shadow-diff/monarchpb"
)

func nodeByID(g *TopologyGraph, id string) *Node {
	for i := range g.Nodes {
		if g.Nodes[i].ID == id {
			return &g.Nodes[i]
		}
	}
	return nil
}

func hasEdge(g *TopologyGraph, source, target string) bool {
	for _, e := range g.Edges {
		if e.Source == source && e.Target == target {
			return true
		}
	}
	return false
}

func TestBuildTopologyGraph_RecordReady(t *testing.T) {
	g := BuildTopologyGraph(&monarchpb.ShadowTestStatusUpdate{
		TestName:    "alpha",
		Namespace:   "default",
		Phase:       monarchpb.Phase_PHASE_READY,
		BootStep:    monarchpb.BootStep_BOOT_STEP_READY,
		Mode:        monarchpb.Mode_MODE_RECORD,
		KaiselPhase: monarchpb.CapturePhase_CAPTURE_PHASE_READY,
		Components: &monarchpb.ComponentStatus{
			IgrisReady: true, ShopReady: true, BeruReady: true,
			KaiselRuleActive: true, AmqpBound: true, TargetDeployment: "checkout-api",
		},
	})

	if g.Mode != "record" || g.Phase != "Ready" || g.BootStep != "Ready" {
		t.Fatalf("header = %s/%s/%s", g.Mode, g.Phase, g.BootStep)
	}
	for _, id := range []string{NodeTargetApp, NodeKaisel, NodeIgris, NodeShop, NodeBeru} {
		if n := nodeByID(g, id); n == nil || n.Status != StatusReady {
			t.Errorf("node %s = %v, want Ready", id, n)
		}
	}
	// Record never provisions the roles; they render greyed out, not missing, so
	// the graph keeps a stable shape across a mode switch.
	for _, role := range monarchpb.ShadowRoles {
		if n := nodeByID(g, role); n == nil || n.Status != StatusDisabled {
			t.Errorf("role %s = %v, want Disabled in record mode", role, n)
		}
	}
	if !hasEdge(g, NodeTargetApp, NodeKaisel) || !hasEdge(g, NodeKaisel, NodeIgris) {
		t.Errorf("record edges missing: %+v", g.Edges)
	}
	if hasEdge(g, NodeIgris, monarchpb.RoleControlA) {
		t.Error("record mode must not wire igris to shadow roles")
	}
	for _, e := range g.Edges {
		if !e.Animated {
			t.Errorf("edge %s should animate when both ends are Ready", e.ID)
		}
	}
}

func TestBuildTopologyGraph_ReplayPartialRoles(t *testing.T) {
	g := BuildTopologyGraph(&monarchpb.ShadowTestStatusUpdate{
		TestName:    "alpha",
		Namespace:   "default",
		Phase:       monarchpb.Phase_PHASE_PROGRESSING,
		BootStep:    monarchpb.BootStep_BOOT_STEP_PROVISIONING_SHADOW,
		Mode:        monarchpb.Mode_MODE_REPLAY,
		KaiselPhase: monarchpb.CapturePhase_CAPTURE_PHASE_DISABLED,
		Components: &monarchpb.ComponentStatus{
			IgrisReady: true, ShopReady: true, BeruReady: true,
			TargetDeployment: "checkout-api",
			ShadowRolesReady: map[string]bool{
				monarchpb.RoleControlA:  true,
				monarchpb.RoleControlB:  true,
				monarchpb.RoleCandidate: false,
			},
		},
	})

	if n := nodeByID(g, monarchpb.RoleControlA); n.Status != StatusReady {
		t.Errorf("control-a = %s, want Ready", n.Status)
	}
	if n := nodeByID(g, monarchpb.RoleCandidate); n.Status != StatusProvisioning {
		t.Errorf("candidate = %s, want Provisioning", n.Status)
	}
	if n := nodeByID(g, NodeKaisel); n.Status != StatusDisabled {
		t.Errorf("kaisel = %s, want Disabled in replay", n.Status)
	}
	if !hasEdge(g, NodeIgris, monarchpb.RoleCandidate) || !hasEdge(g, monarchpb.RoleCandidate, NodeBeru) {
		t.Errorf("replay edges missing: %+v", g.Edges)
	}
	if hasEdge(g, NodeTargetApp, NodeKaisel) {
		t.Error("replay mode must not wire the capture path")
	}

	for _, e := range g.Edges {
		wantAnimated := e.Target != monarchpb.RoleCandidate && e.Source != monarchpb.RoleCandidate
		if e.Animated != wantAnimated {
			t.Errorf("edge %s animated=%v, want %v", e.ID, e.Animated, wantAnimated)
		}
	}
}

// A degraded eBPF tap must be distinguishable from one that is simply off — the
// reason kaisel_phase exists alongside the kaisel_rule_active bool.
func TestBuildTopologyGraph_KaiselDegraded(t *testing.T) {
	g := BuildTopologyGraph(&monarchpb.ShadowTestStatusUpdate{
		Mode:        monarchpb.Mode_MODE_RECORD,
		Phase:       monarchpb.Phase_PHASE_PROGRESSING,
		KaiselPhase: monarchpb.CapturePhase_CAPTURE_PHASE_DEGRADED,
		Components:  &monarchpb.ComponentStatus{KaiselRuleActive: false},
	})
	if n := nodeByID(g, NodeKaisel); n.Status != StatusDegraded {
		t.Fatalf("kaisel = %s, want Degraded", n.Status)
	}
}

func TestBuildTopologyGraph_FailedMarksUnreadyNodes(t *testing.T) {
	g := BuildTopologyGraph(&monarchpb.ShadowTestStatusUpdate{
		Phase:    monarchpb.Phase_PHASE_FAILED,
		BootStep: monarchpb.BootStep_BOOT_STEP_FAILED,
		Mode:     monarchpb.Mode_MODE_RECORD,
		Message:  "beru-local pod: CrashLoopBackOff",
		Components: &monarchpb.ComponentStatus{
			IgrisReady: true, ShopReady: true, BeruReady: false,
			TargetDeployment: "checkout-api",
		},
	})

	if n := nodeByID(g, NodeBeru); n.Status != StatusFailed {
		t.Errorf("beru = %s, want Failed", n.Status)
	}
	// Components that did come up stay Ready — the graph should localise the fault.
	if n := nodeByID(g, NodeShop); n.Status != StatusReady {
		t.Errorf("shop = %s, want Ready", n.Status)
	}
	if g.Message == "" {
		t.Error("failure message should reach the UI")
	}
	for _, e := range g.Edges {
		if e.Target == NodeBeru && e.Animated {
			t.Errorf("edge into a failed node must not animate: %s", e.ID)
		}
	}
}

// Mid-boot, before anything is ready, nothing should claim Ready.
func TestBuildTopologyGraph_MidBoot(t *testing.T) {
	g := BuildTopologyGraph(&monarchpb.ShadowTestStatusUpdate{
		Phase:      monarchpb.Phase_PHASE_PROGRESSING,
		BootStep:   monarchpb.BootStep_BOOT_STEP_PROVISIONING_SINKS,
		Mode:       monarchpb.Mode_MODE_RECORD,
		Components: &monarchpb.ComponentStatus{},
	})

	if g.BootStep != "ProvisioningSinks" {
		t.Fatalf("bootStep = %q", g.BootStep)
	}
	for _, n := range g.Nodes {
		if n.Status == StatusReady {
			t.Errorf("node %s claims Ready mid-boot", n.ID)
		}
	}
	for _, e := range g.Edges {
		if e.Animated {
			t.Errorf("edge %s animates mid-boot", e.ID)
		}
	}
}

func TestBuildTopologyGraph_DeletedIsTombstone(t *testing.T) {
	g := BuildTopologyGraph(&monarchpb.ShadowTestStatusUpdate{
		TestName:  "alpha",
		Namespace: "default",
		Phase:     monarchpb.Phase_PHASE_DELETED,
		Message:   "deleted",
		Components: &monarchpb.ComponentStatus{
			IgrisReady: true, ShopReady: true, BeruReady: true,
		},
	})
	if g.Phase != "Deleted" || g.TestName != "alpha" || g.Namespace != "default" {
		t.Fatalf("tombstone = %+v", g)
	}
	if len(g.Nodes) != 0 || len(g.Edges) != 0 {
		t.Fatalf("tombstone must carry no topology, got %d nodes %d edges", len(g.Nodes), len(g.Edges))
	}
}

func TestBuildTopologyGraph_NilIsNil(t *testing.T) {
	if BuildTopologyGraph(nil) != nil {
		t.Fatal("nil update should produce a nil graph")
	}
}

func TestBuildTopologyGraph_EveryNodeHasALabel(t *testing.T) {
	g := BuildTopologyGraph(&monarchpb.ShadowTestStatusUpdate{
		Mode:       monarchpb.Mode_MODE_REPLAY,
		Components: &monarchpb.ComponentStatus{},
	})
	for _, n := range g.Nodes {
		if n.Label == "" {
			t.Errorf("node %s has no label", n.ID)
		}
	}
}
