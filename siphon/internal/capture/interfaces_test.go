package capture

import (
	"net"
	"testing"
)

func TestIncludeInterfaceBridge(t *testing.T) {
	br := net.Interface{
		Name:  "kindnet0",
		Flags: net.FlagUp,
	}
	if !includeInterface(br) {
		t.Fatal("kindnet0 bridge should be included")
	}
	eth := net.Interface{
		Name:         "eth0",
		Flags:        net.FlagUp,
		HardwareAddr: []byte{0xde, 0xad, 0xbe, 0xef, 0x00, 0x01},
	}
	if !includeInterface(eth) {
		t.Fatal("eth0 should be included")
	}
	lo := net.Interface{
		Name:  "lo",
		Flags: net.FlagUp | net.FlagLoopback,
	}
	if includeInterface(lo) {
		t.Fatal("loopback should be excluded")
	}
}
