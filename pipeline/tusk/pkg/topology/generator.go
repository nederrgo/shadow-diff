// Package topology turns a Monarch status update into a React Flow graph.
//
// The node set and edge set are driven by the ShadowTest's mode and ingress
// drivers: record captures production traffic through Kaisel (HTTP) and/or a
// prod AMQP shadow queue (target→queue→igris; Kaisel only seeds Shop), while
// replay drives the three roles from Igris and never opens the eBPF tap or
// prod broker bind.
package topology

import (
	"github.com/shadow-diff/monarchpb"
	"github.com/shadow-diff/shadowspec"
)

// Node statuses rendered by the UI.
const (
	StatusReady        = "Ready"
	StatusProvisioning = "Provisioning"
	StatusFailed       = "Failed"
	StatusDisabled     = "Disabled"
	StatusDegraded     = "Degraded"
)

// Node IDs. Stable across updates so React Flow can diff by key.
const (
	NodeTargetApp = "target-app"
	NodeKaisel    = "kaisel"
	NodeProdAMQP  = "prod-amqp"
	NodeIgris     = "igris"
	NodeShop      = "shop"
	NodeBeru      = "beru"
)

type Node struct {
	ID     string `json:"id"`
	Type   string `json:"type"`
	Label  string `json:"label"`
	Status string `json:"status"`
}

type Edge struct {
	ID       string `json:"id"`
	Source   string `json:"source"`
	Target   string `json:"target"`
	Animated bool   `json:"animated"`
}

type TopologyGraph struct {
	TestName  string `json:"testName"`
	Namespace string `json:"namespace"`
	Phase     string `json:"phase"`
	BootStep  string `json:"bootStep"`
	Mode      string `json:"mode"`
	Message   string `json:"message,omitempty"`
	Nodes     []Node `json:"nodes"`
	Edges     []Edge `json:"edges"`
}

// edge is an unresolved edge; Animated is computed from endpoint status.
type edge struct{ source, target string }

// recordEdges: HTTP ingress is target→kaisel→igris; AMQP ingress is
// target→prod-amqp→igris. Kaisel never feeds the broker queue — its only
// record-mode sink is Shop (egress seed), plus igris when HTTP is also present.
func recordEdges(drivers []string) []edge {
	hasAMQP := shadowspec.ContainsDriver(drivers, shadowspec.DriverRabbitMQMessage)
	hasHTTP := shadowspec.ContainsDriver(drivers, shadowspec.DriverHTTPRequest)
	edges := []edge{{NodeIgris, NodeBeru}, {NodeKaisel, NodeShop}}
	if hasHTTP || !hasAMQP {
		edges = append(edges,
			edge{NodeTargetApp, NodeKaisel},
			edge{NodeKaisel, NodeIgris},
		)
	}
	if hasAMQP {
		edges = append(edges,
			edge{NodeTargetApp, NodeProdAMQP},
			edge{NodeProdAMQP, NodeIgris},
		)
	}
	return edges
}

// replayEdges is built from the role list so adding a fourth role is one change.
func replayEdges() []edge {
	edges := make([]edge, 0, len(monarchpb.ShadowRoles)*3)
	for _, role := range monarchpb.ShadowRoles {
		edges = append(edges,
			edge{NodeIgris, role},
			edge{role, NodeBeru},
			edge{role, NodeShop},
		)
	}
	return edges
}

var nodeLabels = map[string]string{
	NodeTargetApp:           "Target App",
	NodeKaisel:              "Kaisel (eBPF)",
	NodeProdAMQP:            "Prod AMQP queue",
	NodeIgris:               "Igris (ingress hub)",
	NodeShop:                "Shop (egress mocks)",
	NodeBeru:                "Beru (analysis)",
	monarchpb.RoleControlA:  "control-a",
	monarchpb.RoleControlB:  "control-b",
	monarchpb.RoleCandidate: "candidate",
}

// BuildTopologyGraph projects a status update onto nodes and edges.
func BuildTopologyGraph(u *monarchpb.ShadowTestStatusUpdate) *TopologyGraph {
	if u == nil {
		return nil
	}
	// Tombstone: identity + Deleted phase only. Hub drops the cache entry; the
	// browser clears its canvas when it sees Phase == "Deleted".
	if u.GetPhase() == monarchpb.Phase_PHASE_DELETED {
		return &TopologyGraph{
			TestName:  u.GetTestName(),
			Namespace: u.GetNamespace(),
			Phase:     phaseLabel(u.GetPhase()),
			Message:   u.GetMessage(),
		}
	}
	replay := u.GetMode() == monarchpb.Mode_MODE_REPLAY
	failed := u.GetPhase() == monarchpb.Phase_PHASE_FAILED
	c := u.GetComponents()
	hasAMQP := shadowspec.ContainsDriver(c.GetIngressDrivers(), shadowspec.DriverRabbitMQMessage)

	status := func(ready bool) string {
		switch {
		case ready:
			return StatusReady
		case failed:
			return StatusFailed
		default:
			return StatusProvisioning
		}
	}

	nodes := []Node{
		// The prod Deployment is an external reference, not something Monarch boots;
		// it is Ready whenever it was resolved at all.
		{ID: NodeTargetApp, Type: "target", Status: status(c.GetTargetDeployment() != "")},
		{ID: NodeIgris, Type: "ingress", Status: status(c.GetIgrisReady())},
		{ID: NodeShop, Type: "egress", Status: status(c.GetShopReady())},
		{ID: NodeBeru, Type: "sink", Status: status(c.GetBeruReady())},
		{ID: NodeKaisel, Type: "capture", Status: kaiselStatus(u, replay, failed)},
	}
	if hasAMQP {
		nodes = append(nodes, Node{
			ID:     NodeProdAMQP,
			Type:   "amqp",
			Status: amqpStatus(c.GetAmqpBound(), replay, failed),
		})
	}

	// Shadow roles exist only in replay; in record they render greyed out so the
	// graph keeps a stable shape across a mode switch.
	roles := c.GetShadowRolesReady()
	for _, role := range monarchpb.ShadowRoles {
		st := StatusDisabled
		if replay {
			st = status(roles[role])
		}
		nodes = append(nodes, Node{ID: role, Type: "role", Status: st})
	}

	for i := range nodes {
		if label, ok := nodeLabels[nodes[i].ID]; ok {
			nodes[i].Label = label
		}
	}

	shape := recordEdges(c.GetIngressDrivers())
	if replay {
		shape = replayEdges()
	}
	byID := make(map[string]string, len(nodes))
	for _, n := range nodes {
		byID[n.ID] = n.Status
	}
	edges := make([]Edge, 0, len(shape))
	for _, e := range shape {
		edges = append(edges, Edge{
			ID:     e.source + "->" + e.target,
			Source: e.source,
			Target: e.target,
			// Animate only where traffic can actually flow.
			Animated: byID[e.source] == StatusReady && byID[e.target] == StatusReady,
		})
	}

	return &TopologyGraph{
		TestName:  u.GetTestName(),
		Namespace: u.GetNamespace(),
		Phase:     phaseLabel(u.GetPhase()),
		BootStep:  bootStepLabel(u.GetBootStep()),
		Mode:      modeLabel(u.GetMode()),
		Message:   u.GetMessage(),
		Nodes:     nodes,
		Edges:     edges,
	}
}

// amqpStatus: replay never binds the prod broker; otherwise amqp_bound drives Ready.
func amqpStatus(bound, replay, failed bool) string {
	if replay {
		return StatusDisabled
	}
	if bound {
		return StatusReady
	}
	if failed {
		return StatusFailed
	}
	return StatusProvisioning
}

// kaiselStatus prefers the capture phase over the bool, so a tap that reconciled
// but is failing renders Degraded rather than an indistinct "not ready".
func kaiselStatus(u *monarchpb.ShadowTestStatusUpdate, replay, failed bool) string {
	switch u.GetKaiselPhase() {
	case monarchpb.CapturePhase_CAPTURE_PHASE_READY:
		return StatusReady
	case monarchpb.CapturePhase_CAPTURE_PHASE_DEGRADED:
		return StatusDegraded
	case monarchpb.CapturePhase_CAPTURE_PHASE_DISABLED:
		return StatusDisabled
	}
	if replay {
		return StatusDisabled
	}
	if u.GetComponents().GetKaiselRuleActive() {
		return StatusReady
	}
	if failed {
		return StatusFailed
	}
	return StatusProvisioning
}

func phaseLabel(p monarchpb.Phase) string {
	switch p {
	case monarchpb.Phase_PHASE_PROGRESSING:
		return "Progressing"
	case monarchpb.Phase_PHASE_READY:
		return "Ready"
	case monarchpb.Phase_PHASE_FAILED:
		return "Failed"
	case monarchpb.Phase_PHASE_DELETING:
		return "Deleting"
	case monarchpb.Phase_PHASE_DELETED:
		return "Deleted"
	default:
		return ""
	}
}

func modeLabel(m monarchpb.Mode) string {
	switch m {
	case monarchpb.Mode_MODE_RECORD:
		return "record"
	case monarchpb.Mode_MODE_REPLAY:
		return "replay"
	default:
		return ""
	}
}

func bootStepLabel(b monarchpb.BootStep) string {
	switch b {
	case monarchpb.BootStep_BOOT_STEP_VALIDATING_INPUTS:
		return "ValidatingInputs"
	case monarchpb.BootStep_BOOT_STEP_PROVISIONING_SINKS:
		return "ProvisioningSinks"
	case monarchpb.BootStep_BOOT_STEP_ACTIVATING_EGRESS_TAP:
		return "ActivatingEgressTap"
	case monarchpb.BootStep_BOOT_STEP_BINDING_AMQP:
		return "BindingAMQP"
	case monarchpb.BootStep_BOOT_STEP_PROVISIONING_SHADOW:
		return "ProvisioningShadow"
	case monarchpb.BootStep_BOOT_STEP_READY:
		return "Ready"
	case monarchpb.BootStep_BOOT_STEP_FAILED:
		return "Failed"
	default:
		return ""
	}
}
