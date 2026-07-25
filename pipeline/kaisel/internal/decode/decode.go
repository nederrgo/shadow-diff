package decode

import (
	"encoding/binary"

	"github.com/gopacket/gopacket"
	"github.com/gopacket/gopacket/layers"
)

const ethertypeIPv4 = 0x0800

func layerTypeFor(f Framing) gopacket.LayerType {
	if f == FramingEthernet {
		return layers.LayerTypeEthernet
	}
	return layers.LayerTypeIPv4
}

// plausible reports whether b could be an IPv4 packet under framing f.
//
// This exists because gopacket's IPv4 decoder is lenient: handed the first
// bytes of an Ethernet frame it happily returns a garbage-but-non-nil network
// layer, so "did we get a network layer?" cannot by itself tell a correct
// decode from a wrong-framing one. Checking the version nibble at the framing's
// offset -- the same test capture.c performs in the kernel -- makes the choice
// deterministic instead.
//
// Only IPv4 is recognised, matching the BPF program. VLAN-tagged frames
// (0x8100) are rejected here and equally unmatched in the kernel, so the two
// halves stay consistent.
func plausible(b []byte, f Framing) bool {
	if f == FramingEthernet {
		return len(b) >= 15 &&
			binary.BigEndian.Uint16(b[12:14]) == ethertypeIPv4 &&
			b[14]>>4 == 4
	}
	return len(b) >= 1 && b[0]>>4 == 4
}

// Packet decodes captured bytes, preferring the framing f implied by the
// interface and falling back to the other if the bytes do not fit.
//
// Two independent mechanisms guard the same failure, deliberately. Framing is
// derived from sysfs and is also what the kernel used to locate the IP header,
// so the halves agree by construction. The fallback covers what sysfs cannot
// describe: tunnel devices reporting ARPHRD_ETHER while carrying bare L3 frames.
//
// The final guard is NetworkLayer() == nil, not ErrorLayer() != nil: a TCP
// payload truncated at the capture snaplen sets an error layer but still yields
// a usable network layer, and rejecting those would discard most real traffic.
func Packet(b []byte, f Framing) (gopacket.Packet, bool) {
	for _, cand := range [2]Framing{f, f.other()} {
		if !plausible(b, cand) {
			continue
		}
		p := gopacket.NewPacket(b, layerTypeFor(cand), gopacket.Default)
		if p.NetworkLayer() != nil {
			return p, true
		}
	}
	return nil, false
}
