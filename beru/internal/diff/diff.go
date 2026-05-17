package diff

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
)

// PathDiff describes a single differing JSON path.
type PathDiff struct {
	Path     string
	Expected string
	Actual   string
}

// CompareJSON returns paths where a and b differ (dot/bracket notation).
func CompareJSON(a, b []byte) ([]PathDiff, error) {
	var va, vb any
	if err := json.Unmarshal(a, &va); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &vb); err != nil {
		return nil, err
	}
	var out []PathDiff
	walkCompare("", va, vb, &out)
	return out, nil
}

func walkCompare(prefix string, a, b any, out *[]PathDiff) {
	if jsonEqual(a, b) {
		return
	}
	switch av := a.(type) {
	case map[string]any:
		bv, ok := b.(map[string]any)
		if !ok {
			*out = append(*out, PathDiff{Path: fieldLabel(prefix), Expected: fmt.Sprintf("%v", a), Actual: fmt.Sprintf("%v", b)})
			return
		}
		keys := map[string]struct{}{}
		for k := range av {
			keys[k] = struct{}{}
		}
		for k := range bv {
			keys[k] = struct{}{}
		}
		sorted := make([]string, 0, len(keys))
		for k := range keys {
			sorted = append(sorted, k)
		}
		sort.Strings(sorted)
		for _, k := range sorted {
			p := joinPath(prefix, k)
			walkCompare(p, av[k], bv[k], out)
		}
	case []any:
		bv, ok := b.([]any)
		if !ok {
			*out = append(*out, PathDiff{Path: fieldLabel(prefix), Expected: fmt.Sprintf("%v", a), Actual: fmt.Sprintf("%v", b)})
			return
		}
		max := len(av)
		if len(bv) > max {
			max = len(bv)
		}
		for i := 0; i < max; i++ {
			var ai, bi any
			if i < len(av) {
				ai = av[i]
			}
			if i < len(bv) {
				bi = bv[i]
			}
			p := prefix + "[" + fmt.Sprintf("%d", i) + "]"
			walkCompare(p, ai, bi, out)
		}
	default:
		*out = append(*out, PathDiff{
			Path:     fieldLabel(prefix),
			Expected: fmt.Sprintf("%v", a),
			Actual:   fmt.Sprintf("%v", b),
		})
	}
}

func joinPath(prefix, key string) string {
	if prefix == "" {
		return key
	}
	return prefix + "." + key
}

func fieldLabel(path string) string {
	if path == "" {
		return "(root)"
	}
	if strings.Contains(path, ".") {
		parts := strings.Split(path, ".")
		return parts[len(parts)-1]
	}
	if strings.Contains(path, "[") {
		if idx := strings.LastIndex(path, "."); idx >= 0 {
			return path[idx+1:]
		}
	}
	return path
}

func jsonEqual(a, b any) bool {
	ab, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	return string(ab) == string(bb)
}

// NoisePaths returns paths that differ between control-a and control-b.
func NoisePaths(bodyA, bodyB []byte) (map[string]struct{}, error) {
	diffs, err := CompareJSON(bodyA, bodyB)
	if err != nil {
		return nil, err
	}
	out := make(map[string]struct{}, len(diffs))
	for _, d := range diffs {
		out[d.Path] = struct{}{}
	}
	return out, nil
}

// Regressions returns diffs between control-a and candidate excluding noise paths.
func Regressions(bodyA, bodyC []byte, noise map[string]struct{}) ([]PathDiff, error) {
	diffs, err := CompareJSON(bodyA, bodyC)
	if err != nil {
		return nil, err
	}
	var out []PathDiff
	for _, d := range diffs {
		if _, skip := noise[d.Path]; skip {
			continue
		}
		out = append(out, d)
	}
	return out, nil
}

// Analyze runs diff-of-diffs and logs results.
func Analyze(log *slog.Logger, traceID string, bodyA, bodyB, bodyC []byte, noiseFields []string) {
	if log == nil {
		log = slog.Default()
	}
	noise, err := NoisePaths(bodyA, bodyB)
	if err != nil {
		log.Error("Could not compare control pods for noise", "traceID", traceID, "err", err)
		return
	}
	if len(noise) == 0 {
		log.Info("No noise fields identified for trace", "traceID", traceID)
	}
	regs, err := Regressions(bodyA, bodyC, noise)
	if err != nil {
		log.Error("Could not compare control-a and candidate", "traceID", traceID, "err", err)
		return
	}
	if len(regs) == 0 {
		log.Info(fmt.Sprintf("No regression for Trace %s", traceID))
		return
	}
	ignored := formatIgnoredNoise(noise)
	for _, r := range regs {
		log.Info(fmt.Sprintf(
			"Regression found in Trace %s: Field '%s' expected %s but got %s.%s",
			traceID, r.Path, r.Expected, r.Actual, ignored,
		))
	}
}

func formatIgnoredNoise(noise map[string]struct{}) string {
	if len(noise) == 0 {
		return ""
	}
	names := make([]string, 0, len(noise))
	for p := range noise {
		names = append(names, fieldLabel(p))
	}
	sort.Strings(names)
	if len(names) == 1 {
		return fmt.Sprintf(" (Ignored field '%s' because it was identified as noise.)", names[0])
	}
	return fmt.Sprintf(" (Ignored fields %v because they were identified as noise.)", names)
}
