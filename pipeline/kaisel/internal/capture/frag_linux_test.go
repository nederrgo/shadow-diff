//go:build linux && integration

package capture

import (
	"encoding/binary"
	"net"
	"os"
	"testing"
	"time"

	"github.com/cilium/ebpf/rlimit"
	"golang.org/x/sys/unix"

	"github.com/shadow-diff/kaisel/internal/decode"
)

// A port with no listener: this test only cares what the kernel filter counts,
// never what a server does with the packets.
const fragPort = 8081

// checksum is the standard 16-bit one's-complement header checksum.
func checksum(b []byte) uint16 {
	var sum uint32
	for i := 0; i+1 < len(b); i += 2 {
		sum += uint32(binary.BigEndian.Uint16(b[i:]))
	}
	if len(b)%2 == 1 {
		sum += uint32(b[len(b)-1]) << 8
	}
	for sum>>16 != 0 {
		sum = sum&0xffff + sum>>16
	}
	return ^uint16(sum)
}

// tcpSegment builds a bare TCP header. The checksum is left zero: nothing on
// this path verifies it.
func tcpSegment(sport, dport uint16, payload []byte) []byte {
	h := make([]byte, 20)
	binary.BigEndian.PutUint16(h[0:], sport)
	binary.BigEndian.PutUint16(h[2:], dport)
	binary.BigEndian.PutUint32(h[4:], 1000)
	h[12] = 5 << 4
	h[13] = 0x18 // PSH|ACK
	binary.BigEndian.PutUint16(h[14:], 65535)
	return append(h, payload...)
}

// ipv4 wraps payload in an IPv4 header. fragOff is a byte offset and must be a
// multiple of 8; mf sets More Fragments.
func ipv4(src, dst net.IP, payload []byte, fragOff uint16, mf bool) []byte {
	h := make([]byte, 20)
	h[0] = 0x45
	binary.BigEndian.PutUint16(h[2:], uint16(20+len(payload)))
	binary.BigEndian.PutUint16(h[4:], 0x2A2A)
	flags := fragOff / 8
	if mf {
		flags |= 0x2000
	}
	binary.BigEndian.PutUint16(h[6:], flags)
	h[8] = 64
	h[9] = 6 // TCP
	copy(h[12:], src.To4())
	copy(h[16:], dst.To4())
	binary.BigEndian.PutUint16(h[10:], checksum(h))
	return append(h, payload...)
}

// Fragmented datagrams must be dropped in the kernel and counted, so a flow
// that silently produces no records has a stated cause.
//
// Asserted on the counter rather than on captured records: gopacket independently
// declines to decode a transport layer from any fragment, so "zero records" holds
// with or without this filter and would prove nothing.
//
// Runs entirely on loopback -- no bridge, no namespaces. AF_PACKET taps before
// IP defragmentation, so the fragments are visible to the filter exactly as sent.
func TestFragmentsAreDroppedAndCounted(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root for CAP_BPF and CAP_NET_RAW")
	}
	if err := rlimit.RemoveMemlock(); err != nil {
		t.Fatalf("remove memlock: %v", err)
	}

	spec, err := loadBpf()
	if err != nil {
		t.Fatalf("load spec: %v", err)
	}
	if err := spec.Variables["l2_off"].Set(decode.FramingEthernet.Offset()); err != nil {
		t.Fatal(err)
	}
	if err := spec.Variables["port_filter_on"].Set(uint32(1)); err != nil {
		t.Fatal(err)
	}
	var objs bpfObjects
	if err := spec.LoadAndAssign(&objs, nil); err != nil {
		t.Fatalf("load objects: %v", err)
	}
	defer objs.Close()

	lo := net.ParseIP("127.0.0.1")
	key, ok := ipKey(lo)
	if !ok {
		t.Fatal("127.0.0.1 rejected as IPv4")
	}
	if err := objs.TargetIps.Put(key, uint8(1)); err != nil {
		t.Fatal(err)
	}
	if err := objs.TargetPorts.Put(uint16(fragPort), uint8(1)); err != nil {
		t.Fatal(err)
	}

	iface, err := net.InterfaceByName("lo")
	if err != nil {
		t.Fatal(err)
	}
	sock, err := openRawSocket(iface.Index, objs.Capture.FD())
	if err != nil {
		t.Fatalf("attach filter: %v", err)
	}
	defer unix.Close(sock)

	raw, err := unix.Socket(unix.AF_INET, unix.SOCK_RAW, unix.IPPROTO_RAW)
	if err != nil {
		t.Fatalf("open raw send socket: %v", err)
	}
	defer unix.Close(raw)
	to := &unix.SockaddrInet4{}
	copy(to.Addr[:], lo.To4())

	send := func(pkt []byte) {
		if err := unix.Sendto(raw, pkt, 0, to); err != nil {
			t.Fatalf("send: %v", err)
		}
	}
	count := fragDrops(objs.FragDrops)

	// waitStable lets the counter settle; the filter runs synchronously on the
	// send path, so this converges immediately in practice.
	waitStable := func() uint64 {
		var last uint64
		for i := 0; i < 20; i++ {
			time.Sleep(50 * time.Millisecond)
			if n := count(); n == last && i > 0 {
				return n
			} else {
				last = n
			}
		}
		return last
	}

	seg := tcpSegment(40000, fragPort, []byte("GET /frag HTTP/1.1\r\nHost: x\r\n\r\n"))
	const cut = 24 // multiple of 8, and past the 20-byte TCP header

	base := waitStable()
	send(ipv4(lo, lo, seg[:cut], 0, true))    // first fragment, MF set
	send(ipv4(lo, lo, seg[cut:], cut, false)) // last fragment, non-zero offset
	fragged := waitStable()

	// At least two: loopback taps both the transmit and receive path, so each
	// packet may be seen more than once.
	if got := fragged - base; got < 2 {
		t.Fatalf("fragment drops rose by %d, want at least 2 (both fragments)", got)
	}

	// The discriminating half: an identical but unfragmented datagram must not
	// be counted, proving the filter keys on fragmentation and not on traffic.
	send(ipv4(lo, lo, seg, 0, false))
	if whole := waitStable(); whole != fragged {
		t.Errorf("unfragmented datagram counted as a fragment: %d -> %d", fragged, whole)
	}
}
