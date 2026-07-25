package pxl

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// LabelFilterLines emits PxL rows that keep pods matching targetLabels.
func LabelFilterLines(labels map[string]string) string {
	if len(labels) == 0 {
		return "# no label filters"
	}
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for i, k := range keys {
		if i > 0 {
			b.WriteByte('\n')
		}
		v := labels[k]
		// ponytail: pod name contains app label value (service ctx is often empty on minikube)
		if k == "app" {
			fmt.Fprintf(&b, "df = df[px.contains(df.pod, '%s')]", v)
		} else {
			fmt.Fprintf(&b, "df = df[df.ctx['%s'] == '%s']", k, v)
		}
	}
	return b.String()
}

// RemoteClientFilterLines scopes server-side egress to the worker app pod.
func RemoteClientFilterLines(labels map[string]string) string {
	app := labels["app"]
	if app == "" {
		return "# no remote client filters"
	}
	return fmt.Sprintf("df = df[px.contains(df.client_pod, '%s')]", app)
}

// ExcludePathLines drops req_path matching each regex.
func ExcludePathLines(paths []string) string {
	if len(paths) == 0 {
		return "# no exclude paths"
	}
	var b strings.Builder
	for i, re := range paths {
		if i > 0 {
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, "df = df[not px.regex_match('%s', df.req_path)]", re)
	}
	return b.String()
}

// PortFilterLines keeps the lowest target port (PxL has no vectorized OR).
func PortFilterLines(ports []int32) string {
	if len(ports) == 0 {
		return "# no port filters"
	}
	min := ports[0]
	for _, p := range ports[1:] {
		if p < min {
			min = p
		}
	}
	return fmt.Sprintf("df = df[df.local_port == %d]", min)
}

// HexNibbleExpr builds nested px.select mapping a hex char column to 0-15 (-1 unknown).
func HexNibbleExpr(col string) string {
	e := "-1"
	pairs := []struct {
		ch  string
		val int
	}{
		{"f", 15}, {"e", 14}, {"d", 13}, {"c", 12}, {"b", 11}, {"a", 10},
		{"9", 9}, {"8", 8}, {"7", 7}, {"6", 6}, {"5", 5}, {"4", 4},
		{"3", 3}, {"2", 2}, {"1", 1}, {"0", 0},
	}
	for _, p := range pairs {
		e = fmt.Sprintf("px.select(%s == '%s', %d, %s)", col, p.ch, p.val, e)
	}
	return e
}

// SampleFilterLines emits the shared prod-gate sampling rule with Go siphon:
// V = int(trace_id[0:2], 16); keep iff (V*100) < (N*256).
func SampleFilterLines(pct int, column string) string {
	if column == "" {
		column = "trace_hdr"
	}
	lines := []string{fmt.Sprintf("df = df[df.%s != '']", column)}
	if pct <= 0 || pct >= 100 {
		return lines[0]
	}
	lines = append(lines,
		fmt.Sprintf("df._h0 = px.tolower(px.substring(df.%s, 3, 1))", column),
		fmt.Sprintf("df._h1 = px.tolower(px.substring(df.%s, 4, 1))", column),
		fmt.Sprintf("df._n0 = %s", HexNibbleExpr("df._h0")),
		fmt.Sprintf("df._n1 = %s", HexNibbleExpr("df._h1")),
		"df = df[df._n0 >= 0]",
		"df = df[df._n1 >= 0]",
		"df._sample_v = df._n0 * 16 + df._n1",
		fmt.Sprintf("df = df[(df._sample_v * 100) < (%d * 256)]", pct),
	)
	return strings.Join(lines, "\n")
}

// SamplePct normalizes SamplePercentage (0 / unset → 100).
func SamplePct(n int) int {
	if n <= 0 {
		return 100
	}
	return n
}

// FormatPorts is a tiny helper for tests / debug.
func FormatPorts(ports []int32) string {
	parts := make([]string, len(ports))
	for i, p := range ports {
		parts[i] = strconv.FormatInt(int64(p), 10)
	}
	return strings.Join(parts, ",")
}
