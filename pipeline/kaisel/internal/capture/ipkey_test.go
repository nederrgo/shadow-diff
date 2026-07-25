package capture

import (
	"net"
	"testing"
)

// The Go/C contract for target_ips: the key is the dotted quad's plain numeric
// value, composed byte-wise on both sides so it never depends on node
// endianness. Pinned here because the natural-looking alternative -- loading
// the header bytes straight into a __u32 and mirroring that with
// binary.NativeEndian -- is silently wrong on a big-endian node and matches
// nothing.
func TestIPKey(t *testing.T) {
	for _, tc := range []struct {
		ip   string
		want uint32
	}{
		{"10.99.0.2", 0x0A630002},
		{"0.0.0.0", 0},
		{"255.255.255.255", 0xFFFFFFFF},
		{"203.0.113.5", 0xCB007105},
	} {
		got, ok := ipKey(net.ParseIP(tc.ip))
		if !ok {
			t.Errorf("ipKey(%s) rejected an IPv4 address", tc.ip)
			continue
		}
		if got != tc.want {
			t.Errorf("ipKey(%s) = %#08x, want %#08x", tc.ip, got, tc.want)
		}
	}

	if _, ok := ipKey(net.ParseIP("2001:db8::1")); ok {
		t.Error("ipKey accepted an IPv6 address; the kernel side only reads IPv4 headers")
	}
}
