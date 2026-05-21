package capture

import (
	"encoding/binary"
	"fmt"
	"net"
	"syscall"
	"unsafe"

	"github.com/google/gopacket"
)

const ethPAll = 0x0003 // ETH_P_ALL in host byte order

// rawSockHandle is a minimal AF_PACKET SOCK_RAW socket using recvfrom instead
// of the TPacket mmap ring.  In WSL2/Kind, poll() on TPacket sockets often
// never wakes up; recvfrom with SO_RCVTIMEO works reliably.
type rawSockHandle struct {
	fd    int
	iface string
	buf   []byte
}

func newRawSockHandle(iface string) (*rawSockHandle, error) {
	fd, err := syscall.Socket(syscall.AF_PACKET, syscall.SOCK_RAW, int(rawHtons(ethPAll)))
	if err != nil {
		return nil, fmt.Errorf("socket AF_PACKET %s: %w", iface, err)
	}

	ifaceObj, err := net.InterfaceByName(iface)
	if err != nil {
		syscall.Close(fd)
		return nil, fmt.Errorf("interface %s: %w", iface, err)
	}
	sll := syscall.SockaddrLinklayer{
		Protocol: rawHtons(ethPAll),
		Ifindex:  ifaceObj.Index,
	}
	if err := syscall.Bind(fd, &sll); err != nil {
		syscall.Close(fd)
		return nil, fmt.Errorf("bind %s ifindex=%d: %w", iface, ifaceObj.Index, err)
	}

	tv := syscall.Timeval{Sec: 0, Usec: 100_000} // 100 ms read timeout
	if err := syscall.SetsockoptTimeval(fd, syscall.SOL_SOCKET, syscall.SO_RCVTIMEO, &tv); err != nil {
		syscall.Close(fd)
		return nil, fmt.Errorf("SO_RCVTIMEO on %s: %w", iface, err)
	}

	return &rawSockHandle{fd: fd, iface: iface, buf: make([]byte, defaultSnaplen)}, nil
}

func (h *rawSockHandle) readRaw() ([]byte, error) {
	n, _, err := syscall.Recvfrom(h.fd, h.buf, 0)
	if err != nil {
		e, ok := err.(syscall.Errno)
		if ok && (e == syscall.EAGAIN || e == syscall.EWOULDBLOCK) {
			return nil, nil // timeout; caller retries
		}
		return nil, err
	}
	if n == 0 {
		return nil, nil
	}
	out := make([]byte, n)
	copy(out, h.buf[:n])
	return out, nil
}

// decode parses Ethernet frames delivered by SOCK_RAW on a named interface.
func (h *rawSockHandle) decode(data []byte) gopacket.Packet {
	return decodePacket(data)
}

func (h *rawSockHandle) ifaceName() string { return h.iface }

func (h *rawSockHandle) Close() {
	if h.fd > 0 {
		syscall.Close(h.fd)
		h.fd = 0
	}
}

func rawHtons(v uint16) uint16 {
	b := [2]byte{}
	binary.BigEndian.PutUint16(b[:], v)
	return *(*uint16)(unsafe.Pointer(&b[0]))
}
