package parse

import (
	"strconv"
	"strings"
)

func parserPeek(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	if len(b) > 120 {
		b = b[:120]
	}
	var sb strings.Builder
	for _, c := range b {
		switch {
		case c >= 32 && c < 127:
			sb.WriteByte(c)
		case c == '\r':
			sb.WriteString(`\r`)
		case c == '\n':
			sb.WriteString(`\n`)
		default:
			sb.WriteString(`\x`)
			sb.WriteString(strconv.FormatInt(int64(c), 16))
		}
	}
	return sb.String()
}
