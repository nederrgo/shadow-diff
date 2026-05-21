//go:build !pcap

package capture

import (
	"fmt"

	"github.com/google/gopacket"
)

// pcapAnyHandle stub — not usable, but must satisfy captureHandle so the compiler
// is happy.  newPcapAnyHandle always returns an error so the engine falls back.
type pcapAnyHandle struct{}

func newPcapAnyHandle(bpfFilter string) (*pcapAnyHandle, error) {
	return nil, fmt.Errorf("pcap support not compiled in (build with -tags pcap and install libpcap-dev)")
}

func (h *pcapAnyHandle) readRaw() ([]byte, error)           { return nil, nil }
func (h *pcapAnyHandle) decode([]byte) gopacket.Packet      { return nil }
func (h *pcapAnyHandle) ifaceName() string                  { return AllInterfacesLabel }
func (h *pcapAnyHandle) Close()                             {}
