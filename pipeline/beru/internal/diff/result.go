package diff

const (
	StatusMatch     = "MATCH"
	StatusMismatch  = "MISMATCH"
	ProtocolIngress = "ingress"
)

// Result holds the outcome of a diff-of-diffs analysis.
type Result struct {
	TraceID     string
	Protocol    string
	Status      string
	Regressions []PathDiff
	BodyA       []byte
	BodyC       []byte
	ControlA    [][]byte
	ControlB    [][]byte
	Candidate   [][]byte
	Err         error
}

// MergeNoise combines A/B noise paths with user-configured filter paths.
func MergeNoise(abNoise, userNoise map[string]struct{}) map[string]struct{} {
	if len(abNoise) == 0 && len(userNoise) == 0 {
		return nil
	}
	out := make(map[string]struct{}, len(abNoise)+len(userNoise))
	for p := range abNoise {
		out[p] = struct{}{}
	}
	for p := range userNoise {
		out[p] = struct{}{}
	}
	return out
}
