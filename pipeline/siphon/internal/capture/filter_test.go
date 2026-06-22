package capture

import (
	"testing"

	"github.com/google/gopacket/layers"
	"github.com/shadow-diff/siphon/internal/config"
)

func TestPacketMatchesCapture_ingress(t *testing.T) {
	m := config.NewManager()
	m.Update(config.SiphonConfig{
		Targets: []config.SiphonTarget{{
			TargetIPs:   []string{"10.0.0.1"},
			TargetPorts: []int{80},
		}},
	})
	if !packetMatchesCapture(m, "1.2.3.4", "10.0.0.1", 12345, 80) {
		t.Fatal("expected ingress dst match")
	}
}

func TestPacketMatchesCapture_egressProdIP(t *testing.T) {
	m := config.NewManager()
	m.Update(config.SiphonConfig{
		Targets: []config.SiphonTarget{{
			TargetIPs:   []string{"10.0.0.1"},
			TargetPorts: []int{80},
		}},
	})
	if !packetMatchesCapture(m, "10.0.0.1", "8.8.8.8", 45678, 443) {
		t.Fatal("expected prod src match")
	}
	if !packetMatchesCapture(m, "8.8.8.8", "10.0.0.1", 443, 45678) {
		t.Fatal("expected prod dst match")
	}
}

func TestPacketMatchesCapture_unrelated(t *testing.T) {
	m := config.NewManager()
	m.Update(config.SiphonConfig{
		Targets: []config.SiphonTarget{{
			TargetIPs:   []string{"10.0.0.1"},
			TargetPorts: []int{80},
		}},
	})
	if packetMatchesCapture(m, "1.2.3.4", "5.6.7.8", 80, 443) {
		t.Fatal("unrelated flow should not match")
	}
}

func TestDecodePacket_rawIPv4(t *testing.T) {
	raw := minimalTCPFrame([4]byte{1, 2, 3, 4}, [4]byte{10, 0, 0, 1}, 12345, 80)
	pkt := decodePacket(raw)
	if pkt.Layer(layers.LayerTypeIPv4) == nil {
		t.Fatal("expected IPv4 layer from raw IP frame")
	}
	if pkt.Layer(layers.LayerTypeTCP) == nil {
		t.Fatal("expected TCP layer from raw IP frame")
	}
}
