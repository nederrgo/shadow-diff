//go:build linux && integration

package capture

import (
	"encoding/binary"
	"fmt"
	"net"
	"strings"
	"testing"
)

// Frame construction for the gate tests: Ethernet + IPv4 + TCP + payload, laid
// out exactly as capture.c reads it. Checksums are left zero; the program never
// validates them.

const (
	tcpFIN = 0x01
	tcpSYN = 0x02
	tcpRST = 0x04
	tcpACK = 0x10
)

type seg struct {
	srcIP, dstIP string
	sport, dport uint16
	flags        byte
	// tcpOptBytes pads the TCP header beyond the 20-byte minimum, so a
	// non-default data offset is exercised. Must be a multiple of 4.
	tcpOptBytes int
	payload     []byte
	// rawDoff overrides the data-offset nibble, for the malformed cases.
	rawDoff int
}

func parseIP(t *testing.T, s string) net.IP {
	t.Helper()
	ip := net.ParseIP(s)
	if ip == nil || ip.To4() == nil {
		t.Fatalf("bad IPv4 %q", s)
	}
	return ip
}

func (s seg) build(t *testing.T) []byte {
	t.Helper()
	if s.tcpOptBytes%4 != 0 {
		t.Fatalf("tcpOptBytes must be a multiple of 4, got %d", s.tcpOptBytes)
	}
	tcpLen := 20 + s.tcpOptBytes

	pkt := make([]byte, 0, 14+20+tcpLen+len(s.payload))

	// Ethernet: dst, src, ethertype. Only the 14-byte length matters to the
	// program, which keys off the IP version nibble rather than ethertype.
	pkt = append(pkt, 0xde, 0xad, 0xbe, 0xef, 0x00, 0x01)
	pkt = append(pkt, 0xde, 0xad, 0xbe, 0xef, 0x00, 0x02)
	pkt = append(pkt, 0x08, 0x00)

	ip := make([]byte, 20)
	ip[0] = 0x45 // IPv4, IHL 5 words
	binary.BigEndian.PutUint16(ip[2:], uint16(20+tcpLen+len(s.payload)))
	ip[6], ip[7] = 0, 0 // no fragmentation
	ip[8] = 64          // TTL
	ip[9] = 6           // TCP
	copy(ip[12:], parseIP(t, s.srcIP).To4())
	copy(ip[16:], parseIP(t, s.dstIP).To4())
	pkt = append(pkt, ip...)

	tcp := make([]byte, tcpLen)
	binary.BigEndian.PutUint16(tcp[0:], s.sport)
	binary.BigEndian.PutUint16(tcp[2:], s.dport)
	doff := tcpLen / 4
	if s.rawDoff != 0 {
		doff = s.rawDoff
	}
	tcp[12] = byte(doff) << 4
	tcp[13] = s.flags
	binary.BigEndian.PutUint16(tcp[14:], 65535)
	pkt = append(pkt, tcp...)

	return append(pkt, s.payload...)
}

// httpHead builds a request head whose traceparent sits at roughly padTo bytes
// in, so a test can place it inside or outside the scan window.
func httpHead(method, traceID string, padTo int) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "%s /v1/orders HTTP/1.1\r\nHost: svc.default.svc.cluster.local:8080\r\n", method)
	for b.Len() < padTo {
		// A filler header, chunked so the loop lands near padTo rather than
		// overshooting it wildly.
		b.WriteString("X-Pad: 0123456789abcdef0123456789abcdef\r\n")
	}
	if traceID != "" {
		fmt.Fprintf(&b, "traceparent: 00-%s-00f067aa0ba902b7-01\r\n", traceID)
	}
	b.WriteString("Accept: */*\r\n\r\n")
	return []byte(b.String())
}

// httpResponse is the response half of a transaction.
func httpResponse() []byte {
	return []byte("HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: 2\r\n\r\nok")
}
