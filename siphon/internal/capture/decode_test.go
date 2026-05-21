package capture

import (
	"testing"

	"github.com/google/gopacket"
	"github.com/google/gopacket/layers"
)

func TestDecodePacketAllRawIPv4TCP(t *testing.T) {
	ip := layers.IPv4{
		Version:  4,
		IHL:      5,
		SrcIP:    []byte{10, 244, 0, 110},
		DstIP:    []byte{10, 244, 0, 92},
		Protocol: layers.IPProtocolTCP,
	}
	tcp := layers.TCP{SrcPort: 12345, DstPort: 80}
	buf := gopacket.NewSerializeBuffer()
	opts := gopacket.SerializeOptions{FixLengths: true, ComputeChecksums: true}
	if err := tcp.SetNetworkLayerForChecksum(&ip); err != nil {
		t.Fatal(err)
	}
	if err := gopacket.SerializeLayers(buf, opts, &ip, &tcp); err != nil {
		t.Fatal(err)
	}
	pkt := decodePacketAll(buf.Bytes())
	if pkt == nil || pkt.Layer(layers.LayerTypeTCP) == nil {
		t.Fatal("expected raw L3 TCP decode")
	}
}

func TestDecodeLinuxSLL2(t *testing.T) {
	ip := layers.IPv4{
		Version:  4,
		IHL:      5,
		SrcIP:    []byte{10, 244, 0, 110},
		DstIP:    []byte{10, 244, 0, 92},
		Protocol: layers.IPProtocolTCP,
	}
	tcp := layers.TCP{SrcPort: 12345, DstPort: 80}
	buf := gopacket.NewSerializeBuffer()
	opts := gopacket.SerializeOptions{FixLengths: true, ComputeChecksums: true}
	_ = tcp.SetNetworkLayerForChecksum(&ip)
	_ = gopacket.SerializeLayers(buf, opts, &ip, &tcp)
	l3 := buf.Bytes()
	frame := make([]byte, linuxSLL2HdrLen+len(l3))
	frame[0] = 0x08
	frame[1] = 0x00 // IPv4 ethertype
	copy(frame[linuxSLL2HdrLen:], l3)
	pkt := decodeLinuxSLL2(frame)
	if pkt == nil || pkt.Layer(layers.LayerTypeTCP) == nil {
		t.Fatal("expected SLL2 framed TCP decode")
	}
}
