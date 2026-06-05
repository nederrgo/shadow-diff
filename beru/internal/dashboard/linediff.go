package dashboard

import (
	"bytes"
	"encoding/json"
	"html/template"
	"strings"
)

// DiffLine is one rendered line in a side-by-side diff column.
type DiffLine struct {
	Text  template.HTML
	Class string
}

// RenderLineDiff pretty-prints JSON bodies and returns marked HTML lines.
func RenderLineDiff(bodyA, bodyC []byte) (left, right []DiffLine) {
	aLines := strings.Split(prettyJSON(bodyA), "\n")
	cLines := strings.Split(prettyJSON(bodyC), "\n")
	lcs := longestCommonSubsequence(aLines, cLines)

	ai, ci := 0, 0
	for _, common := range lcs {
		for ai < len(aLines) && aLines[ai] != common {
			left = append(left, DiffLine{Text: template.HTML(template.HTMLEscapeString(aLines[ai])), Class: "bg-red-100"})
			ai++
		}
		for ci < len(cLines) && cLines[ci] != common {
			right = append(right, DiffLine{Text: template.HTML(template.HTMLEscapeString(cLines[ci])), Class: "bg-green-100"})
			ci++
		}
		left = append(left, DiffLine{Text: template.HTML(template.HTMLEscapeString(common)), Class: ""})
		right = append(right, DiffLine{Text: template.HTML(template.HTMLEscapeString(common)), Class: ""})
		ai++
		ci++
	}
	for ai < len(aLines) {
		left = append(left, DiffLine{Text: template.HTML(template.HTMLEscapeString(aLines[ai])), Class: "bg-red-100"})
		ai++
	}
	for ci < len(cLines) {
		right = append(right, DiffLine{Text: template.HTML(template.HTMLEscapeString(cLines[ci])), Class: "bg-green-100"})
		ci++
	}
	padLines(&left, &right)
	return left, right
}

func padLines(left, right *[]DiffLine) {
	for len(*left) < len(*right) {
		*left = append(*left, DiffLine{Text: "&nbsp;", Class: "bg-gray-50"})
	}
	for len(*right) < len(*left) {
		*right = append(*right, DiffLine{Text: "&nbsp;", Class: "bg-gray-50"})
	}
}

func prettyJSON(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	if !json.Valid(b) {
		return string(b)
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, b, "", "  "); err != nil {
		return string(b)
	}
	return buf.String()
}

func longestCommonSubsequence(a, b []string) []string {
	m, n := len(a), len(b)
	dp := make([][]int, m+1)
	for i := range dp {
		dp[i] = make([]int, n+1)
	}
	for i := 1; i <= m; i++ {
		for j := 1; j <= n; j++ {
			if a[i-1] == b[j-1] {
				dp[i][j] = dp[i-1][j-1] + 1
			} else if dp[i-1][j] >= dp[i][j-1] {
				dp[i][j] = dp[i-1][j]
			} else {
				dp[i][j] = dp[i][j-1]
			}
		}
	}
	out := make([]string, 0, dp[m][n])
	i, j := m, n
	for i > 0 && j > 0 {
		if a[i-1] == b[j-1] {
			out = append(out, a[i-1])
			i--
			j--
		} else if dp[i-1][j] >= dp[i][j-1] {
			i--
		} else {
			j--
		}
	}
	for left, right := 0, len(out)-1; left < right; left, right = left+1, right-1 {
		out[left], out[right] = out[right], out[left]
	}
	return out
}
