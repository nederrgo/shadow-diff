package capture

import (
	"net"

	"github.com/google/gopacket"
	"github.com/google/gopacket/layers"
)

// MatchTargets returns true when the packet is TCP involving a target IP and port (either direction).
func MatchTargets(packet gopacket.Packet, ips map[string]struct{}, ports map[int]struct{}) bool {
	if len(ips) == 0 || len(ports) == 0 {
		return false
	}
	netLayer := packet.NetworkLayer()
	tcpLayer := packet.Layer(layers.LayerTypeTCP)
	if netLayer == nil || tcpLayer == nil {
		return false
	}
	tcp, ok := tcpLayer.(*layers.TCP)
	if !ok {
		return false
	}
	dstIP := dstIPString(netLayer)
	srcIP := srcIPString(netLayer)
	if dstIP != "" {
		if _, ipOK := ips[dstIP]; ipOK {
			if _, portOK := ports[int(tcp.DstPort)]; portOK {
				return true
			}
		}
	}
	if srcIP != "" {
		if _, ipOK := ips[srcIP]; ipOK {
			if _, portOK := ports[int(tcp.SrcPort)]; portOK {
				return true
			}
		}
	}
	return false
}

func srcIPString(netLayer gopacket.NetworkLayer) string {
	switch l := netLayer.(type) {
	case *layers.IPv4:
		return l.SrcIP.String()
	case *layers.IPv6:
		return l.SrcIP.String()
	default:
		src, _ := netLayer.NetworkFlow().Endpoints()
		raw := src.Raw()
		if len(raw) == 4 || len(raw) == 16 {
			return net.IP(raw).String()
		}
		return src.String()
	}
}

func dstIPString(netLayer gopacket.NetworkLayer) string {
	switch l := netLayer.(type) {
	case *layers.IPv4:
		return l.DstIP.String()
	case *layers.IPv6:
		return l.DstIP.String()
	default:
		_, dst := netLayer.NetworkFlow().Endpoints()
		raw := dst.Raw()
		if len(raw) == 4 {
			return net.IP(raw).String()
		}
		if len(raw) == 16 {
			return net.IP(raw).String()
		}
		return dst.String()
	}
}

// TargetSets builds lookup maps from capture target lists.
func TargetSets(ips []string, ports []int) (map[string]struct{}, map[int]struct{}) {
	ipSet := make(map[string]struct{}, len(ips))
	for _, ip := range ips {
		ipSet[ip] = struct{}{}
	}
	portSet := make(map[int]struct{}, len(ports))
	for _, p := range ports {
		portSet[p] = struct{}{}
	}
	return ipSet, portSet
}
