package capture

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/google/gopacket"
	"github.com/google/gopacket/afpacket"
)

const defaultSnaplen = 65536

// rawReader abstracts the underlying capture source so the engine works with
// both AF_PACKET (per named interface) and pcap "any" (all interfaces).
type rawReader interface {
	readRaw() ([]byte, error)
	Close()
}

// packetHandle wraps an AF_PACKET TPacket socket for a single named interface.
type packetHandle struct {
	tpacket *afpacket.TPacket
	iface   string
	snaplen int
}

func newPacketHandle(iface string) (*packetHandle, error) {
	h, err := afpacket.NewTPacket(
		afpacket.OptInterface(iface),
		afpacket.OptFrameSize(defaultSnaplen),
		afpacket.OptBlockSize(1<<20),
		afpacket.OptNumBlocks(8),
		afpacket.OptPollTimeout(100*time.Millisecond),
		afpacket.SocketRaw,
		afpacket.TPacketVersion2,
	)
	if err != nil {
		return nil, fmt.Errorf("afpacket on %s: %w", iface, err)
	}
	return &packetHandle{tpacket: h, iface: iface, snaplen: defaultSnaplen}, nil
}

func (h *packetHandle) readRaw() ([]byte, error) {
	data, _, err := h.tpacket.ReadPacketData()
	return data, err
}

func (h *packetHandle) Close() {
	if h.tpacket != nil {
		h.tpacket.Close()
	}
}

// decode parses Ethernet frames (SOCK_RAW on a named interface).
func (h *packetHandle) decode(data []byte) gopacket.Packet {
	return decodePacket(data)
}

// ifaceName returns the interface label for logging.
func (h *packetHandle) ifaceName() string { return h.iface }

// attachKernelBPF installs a socket-level BPF filter on a named-interface handle.
// Skipped when either ips or ports is empty — userspace MatchTargets handles filtering.
func (h *packetHandle) attachKernelBPF(log *slog.Logger, ips []string, ports []int) {
	// Temporarily disabled: kernel BPF was filtering out frames even when veth traffic
	// is confirmed present. Userspace MatchTargets provides equivalent correctness.
	if log != nil {
		log.Info("kernel BPF skipped; relying on userspace filter", "iface", h.iface)
	}
}
