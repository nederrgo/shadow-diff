package capture

import (
	"encoding/binary"

	"github.com/google/gopacket"
	"github.com/google/gopacket/layers"
)

const linuxSLL2HdrLen = 20

// decodePacket parses frames from a single named interface (typically Ethernet).
func decodePacket(data []byte) gopacket.Packet {
	return decodeWithLinkTypes(data,
		layers.LayerTypeEthernet,
		layers.LayerTypeLinuxSLL,
	)
}

// decodePacketAll parses frames from an all-interfaces AF_PACKET SOCK_DGRAM socket.
// SOCK_DGRAM (tcpdump -i any) delivers 16-byte Linux SLL cooked headers, so try SLL first.
// Fall back to Ethernet (per-veth fallback), SLL2, then raw L3.
func decodePacketAll(data []byte) gopacket.Packet {
	if pkt := decodeWithLinkTypes(data,
		layers.LayerTypeLinuxSLL,
		layers.LayerTypeEthernet,
	); pkt != nil {
		return pkt
	}
	if pkt := decodeLinuxSLL2(data); pkt != nil {
		return pkt
	}
	if pkt := decodeL3At(data, 0); pkt != nil {
		return pkt
	}
	return nil
}

func decodeWithLinkTypes(data []byte, linkTypes ...gopacket.LayerType) gopacket.Packet {
	for _, lt := range linkTypes {
		pkt := gopacket.NewPacket(data, lt, gopacket.Default)
		if pkt.NetworkLayer() != nil && pkt.Layer(layers.LayerTypeTCP) != nil {
			return pkt
		}
	}
	return nil
}

func decodeL3At(data []byte, offset int) gopacket.Packet {
	if offset >= len(data) {
		return nil
	}
	payload := data[offset:]
	for _, lt := range []gopacket.LayerType{layers.LayerTypeIPv4, layers.LayerTypeIPv6} {
		pkt := gopacket.NewPacket(payload, lt, gopacket.Default)
		if pkt.NetworkLayer() != nil && pkt.Layer(layers.LayerTypeTCP) != nil {
			return pkt
		}
	}
	return nil
}

// decodeLinuxSLL2 handles LINUX_SLL2 (DLT 276): 20-byte cooked header, ethertype at bytes 0-1.
func decodeLinuxSLL2(data []byte) gopacket.Packet {
	if len(data) <= linuxSLL2HdrLen {
		return nil
	}
	if binary.BigEndian.Uint16(data[0:2]) != uint16(layers.EthernetTypeIPv4) &&
		binary.BigEndian.Uint16(data[0:2]) != uint16(layers.EthernetTypeIPv6) {
		return nil
	}
	return decodeL3At(data, linuxSLL2HdrLen)
}
