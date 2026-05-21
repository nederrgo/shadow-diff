package capture

import (
	"testing"

	"github.com/google/gopacket"
	"github.com/google/gopacket/layers"
)

func TestMatchTargets(t *testing.T) {
	ip := layers.IPv4{SrcIP: []byte{10, 0, 0, 1}, DstIP: []byte{10, 0, 0, 2}, Protocol: layers.IPProtocolTCP}
	tcp := layers.TCP{SrcPort: 12345, DstPort: 8080}
	buf := gopacket.NewSerializeBuffer()
	opts := gopacket.SerializeOptions{FixLengths: true}
	_ = gopacket.SerializeLayers(buf, opts, &ip, &tcp)
	packet := gopacket.NewPacket(buf.Bytes(), layers.LayerTypeIPv4, gopacket.Default)

	ips, ports := TargetSets([]string{"10.0.0.2"}, []int{8080})
	if !MatchTargets(packet, ips, ports) {
		t.Fatal("expected match")
	}
	ips2, ports2 := TargetSets([]string{"10.0.0.3"}, []int{8080})
	if MatchTargets(packet, ips2, ports2) {
		t.Fatal("expected no match for wrong IP")
	}
	// Response direction: src=target:port
	ips3, ports3 := TargetSets([]string{"10.0.0.2"}, []int{8080})
	ip2 := layers.IPv4{SrcIP: []byte{10, 0, 0, 2}, DstIP: []byte{10, 0, 0, 1}, Protocol: layers.IPProtocolTCP}
	tcp2 := layers.TCP{SrcPort: 8080, DstPort: 12345}
	buf2 := gopacket.NewSerializeBuffer()
	_ = gopacket.SerializeLayers(buf2, opts, &ip2, &tcp2)
	packet2 := gopacket.NewPacket(buf2.Bytes(), layers.LayerTypeIPv4, gopacket.Default)
	if !MatchTargets(packet2, ips3, ports3) {
		t.Fatal("expected match on response direction")
	}
}
