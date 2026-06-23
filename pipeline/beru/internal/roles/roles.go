package roles

const (
	ControlA   = "control-a"
	ControlB   = "control-b"
	Candidate  = "candidate"
)

// All is the set of shadow pod roles required for diff-of-diffs.
var All = []string{ControlA, ControlB, Candidate}

// IsValid reports whether role is a known shadow role.
func IsValid(role string) bool {
	switch role {
	case ControlA, ControlB, Candidate:
		return true
	default:
		return false
	}
}
