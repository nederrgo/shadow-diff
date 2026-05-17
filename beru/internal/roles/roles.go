package roles

const (
	ControlA   = "control-a"
	ControlB   = "control-b"
	Candidate  = "candidate"
)

// All is the set of shadow pod roles required for diff-of-diffs.
var All = []string{ControlA, ControlB, Candidate}
