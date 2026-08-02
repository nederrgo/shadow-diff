// Package monarchpb carries the gRPC status contract shared by Monarch (producer)
// and Tusk (consumer). It holds the generated protobuf types plus the string
// vocabulary that is not expressible as an enum — currently the shadow role names,
// which travel as map keys on ComponentStatus.ShadowRolesReady.
package monarchpb

// Shadow role names. These are the keys of ComponentStatus.ShadowRolesReady, so
// both sides must agree on them exactly. Monarch's internal role constants are
// defined from these; a mismatch is a compile error rather than an empty node.
const (
	RoleControlA  = "control-a"
	RoleControlB  = "control-b"
	RoleCandidate = "candidate"
)

// ShadowRoles lists every shadow role in diff order: the two controls first, then
// the candidate. Iterate this instead of ranging the map when render order matters.
var ShadowRoles = []string{RoleControlA, RoleControlB, RoleCandidate}
