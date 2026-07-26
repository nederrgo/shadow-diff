// Package decode turns captured packet bytes into reassembled HTTP requests.
//
// Nothing here touches eBPF or the kernel, so the whole package is testable
// without root. internal/capture supplies the bytes; this package is what
// Step 2 grows into an HTTPRecord builder.
package decode

// ARPHRD values from <linux/if_arp.h>. Only the two Ethernet-framed ones matter;
// everything else is treated as raw L3.
const (
	arphrdEther    = 1   // ARPHRD_ETHER
	arphrdLoopback = 772 // ARPHRD_LOOPBACK -- still carries a 14-byte Ethernet header
)

// Framing is the link-layer header size in bytes, i.e. the offset at which the
// IP header starts. The same value is pushed into the BPF program's l2_off
// .rodata constant, so the kernel and user space cannot disagree about where
// to find the IP addresses.
type Framing uint32

const (
	// FramingRawIP is an interface with no link-layer header: the frame starts
	// at the IP header. ARPHRD_NONE tunnels (IPIP, WireGuard, some CNI setups).
	FramingRawIP Framing = 0
	// FramingEthernet is a standard 14-byte Ethernet header.
	FramingEthernet Framing = 14
)

// Offset returns the byte offset of the IP header within a captured frame.
func (f Framing) Offset() uint32 { return uint32(f) }

// other returns the opposite framing, used as the decode fallback.
func (f Framing) other() Framing {
	if f == FramingEthernet {
		return FramingRawIP
	}
	return FramingEthernet
}

func (f Framing) String() string {
	if f == FramingEthernet {
		return "ethernet"
	}
	return "rawip"
}

// FramingFromARPHRD maps an interface's sysfs type to its framing.
//
// Loopback reports its own ARPHRD but is still Ethernet-framed, which is why it
// is not simply "anything that isn't ARPHRD_ETHER is raw IP".
func FramingFromARPHRD(t uint32) Framing {
	switch t {
	case arphrdEther, arphrdLoopback:
		return FramingEthernet
	default:
		return FramingRawIP
	}
}
