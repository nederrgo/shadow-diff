package ingest

import (
	"strconv"
	"strings"
)

// payloadPreview returns a log-safe ASCII snippet of raw TCP/HTTP bytes.
func payloadPreview(b []byte, max int) string {
	if len(b) == 0 {
		return ""
	}
	if max > 0 && len(b) > max {
		b = b[:max]
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
