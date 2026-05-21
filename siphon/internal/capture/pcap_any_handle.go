//go:build pcap

package capture

import (
	"fmt"
	"time"

	"github.com/google/gopacket"
	"github.com/google/gopacket/pcap"
)

// pcapAnyHandle opens libpcap on the "any" pseudo-interface — the same path
// tcpdump -i any uses.  It reliably delivers packets in Kind/Docker/WSL2
// environments where AF_PACKET with ifindex=0 does not work.
type pcapAnyHandle struct {
	handle *pcap.Handle
}

func newPcapAnyHandle(_ string) (*pcapAnyHandle, error) {
	// Open the pseudo "any" interface in promiscuous mode.
	// Do NOT install a kernel BPF filter here: libpcap compiles BPF against
	// DLT_LINUX_SLL for the "any" device, but in some kernel/container
	// combinations the resulting filter drops all frames.
	// Userspace MatchTargets in the engine provides equivalent filtering.
	h, err := pcap.OpenLive("any", defaultSnaplen, true, 100*time.Millisecond)
	if err != nil {
		return nil, fmt.Errorf("pcap open any: %w", err)
	}
	return &pcapAnyHandle{handle: h}, nil
}

func (h *pcapAnyHandle) readRaw() ([]byte, error) {
	data, _, err := h.handle.ReadPacketData()
	if err == pcap.NextErrorTimeoutExpired {
		return nil, nil // no packet yet; caller retries
	}
	return data, err
}

func (h *pcapAnyHandle) Close() {
	if h.handle != nil {
		h.handle.Close()
	}
}

func (h *pcapAnyHandle) ifaceName() string { return AllInterfacesLabel }

func (h *pcapAnyHandle) decode(data []byte) gopacket.Packet {
	// pcap "any" on Linux delivers Linux SLL (DLT_LINUX_SLL) cooked headers.
	return decodePacketAll(data)
}
