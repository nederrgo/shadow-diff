package dashboard

import (
	"fmt"
	"strings"

	v2storage "github.com/shadow-diff/beru/internal/v2/storage"
)

func filterByProtocolAndDirection(reports []v2storage.RawReport, protocol string, direction v2storage.PayloadDirection) []v2storage.RawReport {
	var out []v2storage.RawReport
	for _, r := range reports {
		if r.Protocol == protocol && r.Direction == direction {
			out = append(out, r)
		}
	}
	return out
}

func defaultHTTPDirection() v2storage.PayloadDirection {
	return v2storage.DirectionIngress
}

func normalizeHTTPDirection(protocol, direction string) string {
	if protocol != "http" {
		return ""
	}
	direction = strings.TrimSpace(direction)
	if direction == string(v2storage.DirectionIngress) || direction == string(v2storage.DirectionEgress) {
		return direction
	}
	return string(defaultHTTPDirection())
}

func protocolViewLabel(protocol string, direction v2storage.PayloadDirection) string {
	if protocol == "http" && direction != "" {
		return fmt.Sprintf("http (%s)", direction)
	}
	return protocol
}

func isEgressView(protocol, direction string) bool {
	if protocol == "http" {
		return direction == string(v2storage.DirectionEgress)
	}
	return isEgressProtocol(protocol)
}

func sameTraceView(traceID, protocol, direction string, s v2storage.TraceSummary) bool {
	if s.TraceID != traceID || s.Protocol != protocol {
		return false
	}
	if protocol == "http" {
		return string(s.Direction) == direction
	}
	return true
}
