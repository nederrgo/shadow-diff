package capture

import "github.com/shadow-diff/siphon/internal/config"

// packetMatchesCapture mirrors the old tcp+prod-IP BPF filter: ingress host:port OR src/dst prod IP.
func packetMatchesCapture(m *config.Manager, srcIP, dstIP string, srcPort, dstPort int) bool {
	if m.IsTarget(dstIP, dstPort) {
		return true
	}
	if m.IsProdPodIP(srcIP) || m.IsProdPodIP(dstIP) {
		return true
	}
	return false
}
