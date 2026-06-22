package capture

import "encoding/binary"

// minimalTCPFrame builds a raw IPv4+TCP packet (no Ethernet header) for tests.
func minimalTCPFrame(srcIP, dstIP [4]byte, srcPort, dstPort uint16) []byte {
	const ipLen = 40
	pkt := make([]byte, ipLen)
	pkt[0] = 0x45
	pkt[2] = byte(ipLen >> 8)
	pkt[3] = byte(ipLen)
	pkt[9] = 6
	copy(pkt[12:16], srcIP[:])
	copy(pkt[16:20], dstIP[:])
	pkt[20] = byte(srcPort >> 8)
	pkt[21] = byte(srcPort)
	pkt[22] = byte(dstPort >> 8)
	pkt[23] = byte(dstPort)
	pkt[32] = 0x50
	return pkt
}

func wrapPcapRecord(frame []byte) []byte {
	hdr := make([]byte, 16)
	binary.LittleEndian.PutUint32(hdr[8:12], uint32(len(frame)))
	return append(hdr, frame...)
}
