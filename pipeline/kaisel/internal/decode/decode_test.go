package decode

import (
	"net"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/gopacket/gopacket"
	"github.com/gopacket/gopacket/layers"
	"github.com/gopacket/gopacket/tcpassembly"
)

const wantURI = "/hello"

// buildFrame serialises Ethernet/IPv4/TCP + an HTTP request. withEth controls
// whether the Ethernet header is present, which is what distinguishes a normal
// interface from an ARPHRD_NONE one.
func buildFrame(t *testing.T, withEth bool) []byte {
	t.Helper()

	payload := "GET " + wantURI + " HTTP/1.1\r\nHost: 127.0.0.1:8000\r\n\r\n"
	ip := &layers.IPv4{
		Version: 4, IHL: 5, TTL: 64, Protocol: layers.IPProtocolTCP,
		SrcIP: net.IP{127, 0, 0, 1}, DstIP: net.IP{127, 0, 0, 1},
	}
	tcp := &layers.TCP{SrcPort: 5001, DstPort: 8000, Seq: 1, SYN: false, PSH: true, ACK: true, Window: 65535}
	if err := tcp.SetNetworkLayerForChecksum(ip); err != nil {
		t.Fatalf("checksum layer: %v", err)
	}

	buf := gopacket.NewSerializeBuffer()
	opts := gopacket.SerializeOptions{FixLengths: true, ComputeChecksums: true}

	var err error
	if withEth {
		eth := &layers.Ethernet{
			SrcMAC: net.HardwareAddr{0, 0, 0, 0, 0, 1}, DstMAC: net.HardwareAddr{0, 0, 0, 0, 0, 2},
			EthernetType: layers.EthernetTypeIPv4,
		}
		err = gopacket.SerializeLayers(buf, opts, eth, ip, tcp, gopacket.Payload(payload))
	} else {
		err = gopacket.SerializeLayers(buf, opts, ip, tcp, gopacket.Payload(payload))
	}
	if err != nil {
		t.Fatalf("serialize: %v", err)
	}
	return buf.Bytes()
}

// assemble drives the exact path internal/capture uses: Packet() -> assembler
// -> StreamFactory -> http.ReadRequest, and returns the parsed requests.
func assemble(t *testing.T, frame []byte, f Framing) []*http.Request {
	t.Helper()

	var (
		mu   sync.Mutex
		reqs []*http.Request
	)
	factory := &StreamFactory{OnRequest: func(_, _ gopacket.Flow, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		reqs = append(reqs, r)
	}}
	asm := tcpassembly.NewAssembler(tcpassembly.NewStreamPool(factory))

	p, ok := Packet(frame, f)
	if !ok {
		return nil
	}
	tcp, isTCP := p.TransportLayer().(*layers.TCP)
	if !isTCP {
		t.Fatal("no TCP layer")
	}
	asm.AssembleWithTimestamp(p.NetworkLayer().NetworkFlow(), tcp, time.Now())
	asm.FlushAll() // force the stream closed so ReadRequest sees EOF

	// The parse runs on the factory's goroutine; give it a moment to land.
	for i := 0; i < 100; i++ {
		mu.Lock()
		n := len(reqs)
		mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	return reqs
}

func TestPacketDecodesHTTPAcrossFramings(t *testing.T) {
	tests := []struct {
		name    string
		withEth bool
		framing Framing
	}{
		{"ethernet frame, ethernet framing", true, FramingEthernet},
		{"raw ip frame, rawip framing", false, FramingRawIP},
		// Framing deliberately wrong: proves the fallback recovers rather than
		// merely compiling. This is the case a tunnel reporting ARPHRD_ETHER hits.
		{"ethernet frame, framing mis-set to rawip", true, FramingRawIP},
		{"raw ip frame, framing mis-set to ethernet", false, FramingEthernet},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			reqs := assemble(t, buildFrame(t, tc.withEth), tc.framing)
			if len(reqs) != 1 {
				t.Fatalf("got %d requests, want 1", len(reqs))
			}
			if reqs[0].RequestURI != wantURI {
				t.Errorf("RequestURI = %q, want %q", reqs[0].RequestURI, wantURI)
			}
			if reqs[0].Method != http.MethodGet {
				t.Errorf("Method = %q, want GET", reqs[0].Method)
			}
			if reqs[0].Host != "127.0.0.1:8000" {
				t.Errorf("Host = %q, want 127.0.0.1:8000", reqs[0].Host)
			}
		})
	}
}

// An IPv4 fragment must never yield a TCP layer.
//
// This is what actually keeps fragments out of the assembler, and it is a
// property of gopacket rather than of anything in this repo -- hence pinned
// here. A fragment carries only part of a TCP segment: delivering the first
// fragment's payload would hand the assembler fewer bytes than the sequence
// numbers claim, desynchronising the stream. capture.consume skips any packet
// whose transport layer is not TCP, so declining to decode one is the whole
// safety property.
func TestPacketDoesNotDecodeFragmentsAsTCP(t *testing.T) {
	for _, tc := range []struct {
		name    string
		flags   layers.IPv4Flag
		fragOff uint16
		wantTCP bool
	}{
		{"unfragmented", 0, 0, true},
		{"first fragment", layers.IPv4MoreFragments, 0, false},
		{"later fragment", 0, 7, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ip := &layers.IPv4{
				Version: 4, IHL: 5, TTL: 64, Protocol: layers.IPProtocolTCP,
				SrcIP: net.IP{127, 0, 0, 1}, DstIP: net.IP{127, 0, 0, 1},
				Flags: tc.flags, FragOffset: tc.fragOff,
			}
			tcp := &layers.TCP{SrcPort: 5001, DstPort: 8000, Seq: 1, PSH: true, ACK: true, Window: 65535}
			if err := tcp.SetNetworkLayerForChecksum(ip); err != nil {
				t.Fatalf("checksum layer: %v", err)
			}
			eth := &layers.Ethernet{
				SrcMAC: net.HardwareAddr{0, 0, 0, 0, 0, 1}, DstMAC: net.HardwareAddr{0, 0, 0, 0, 0, 2},
				EthernetType: layers.EthernetTypeIPv4,
			}
			buf := gopacket.NewSerializeBuffer()
			if err := gopacket.SerializeLayers(buf,
				gopacket.SerializeOptions{FixLengths: true, ComputeChecksums: true},
				eth, ip, tcp, gopacket.Payload("GET "+wantURI+" HTTP/1.1\r\nHost: x\r\n\r\n")); err != nil {
				t.Fatalf("serialize: %v", err)
			}

			p, ok := Packet(buf.Bytes(), FramingEthernet)
			if !ok {
				t.Fatal("Packet rejected a well-formed IPv4 frame")
			}
			_, gotTCP := p.TransportLayer().(*layers.TCP)
			if gotTCP != tc.wantTCP {
				t.Errorf("decoded as TCP = %v, want %v", gotTCP, tc.wantTCP)
			}
		})
	}
}

func TestPacketRejectsGarbage(t *testing.T) {
	if _, ok := Packet([]byte{0xde, 0xad, 0xbe, 0xef}, FramingEthernet); ok {
		t.Error("Packet accepted non-IP bytes")
	}
}

func TestFramingFromARPHRD(t *testing.T) {
	tests := []struct {
		arphrd uint32
		want   Framing
	}{
		{1, FramingEthernet},   // ARPHRD_ETHER
		{772, FramingEthernet}, // ARPHRD_LOOPBACK -- Ethernet-framed despite its own type
		{65534, FramingRawIP},  // ARPHRD_NONE -- tunnels, WireGuard
		{768, FramingRawIP},    // ARPHRD_TUNNEL (IPIP)
	}
	for _, tc := range tests {
		if got := FramingFromARPHRD(tc.arphrd); got != tc.want {
			t.Errorf("FramingFromARPHRD(%d) = %v, want %v", tc.arphrd, got, tc.want)
		}
	}
}

func TestFramingOffsetAndOther(t *testing.T) {
	if FramingEthernet.Offset() != 14 || FramingRawIP.Offset() != 0 {
		t.Fatal("offsets must match the l2_off values pushed to the BPF program")
	}
	if FramingEthernet.other() != FramingRawIP || FramingRawIP.other() != FramingEthernet {
		t.Error("other() must toggle")
	}
}
