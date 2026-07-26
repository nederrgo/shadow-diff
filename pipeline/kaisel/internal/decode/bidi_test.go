package decode

import (
	"io"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/gopacket/gopacket"
	"github.com/gopacket/gopacket/layers"
	"github.com/gopacket/gopacket/tcpassembly"
)

var (
	clientIP = net.IP{10, 0, 0, 1}
	serverIP = net.IP{10, 0, 0, 2}
)

const (
	clientPort = layers.TCPPort(40001)
	serverPort = layers.TCPPort(8080)
)

// dirFrame serialises one direction's TCP segment carrying payload.
func dirFrame(t *testing.T, srcIP, dstIP net.IP, sp, dp layers.TCPPort, seq uint32, payload string) []byte {
	t.Helper()
	ip := &layers.IPv4{
		Version: 4, IHL: 5, TTL: 64, Protocol: layers.IPProtocolTCP,
		SrcIP: srcIP, DstIP: dstIP,
	}
	tcp := &layers.TCP{SrcPort: sp, DstPort: dp, Seq: seq, PSH: true, ACK: true, Window: 65535}
	if err := tcp.SetNetworkLayerForChecksum(ip); err != nil {
		t.Fatalf("checksum layer: %v", err)
	}
	eth := &layers.Ethernet{
		SrcMAC: net.HardwareAddr{0, 0, 0, 0, 0, 1}, DstMAC: net.HardwareAddr{0, 0, 0, 0, 0, 2},
		EthernetType: layers.EthernetTypeIPv4,
	}
	buf := gopacket.NewSerializeBuffer()
	opts := gopacket.SerializeOptions{FixLengths: true, ComputeChecksums: true}
	if err := gopacket.SerializeLayers(buf, opts, eth, ip, tcp, gopacket.Payload(payload)); err != nil {
		t.Fatalf("serialize: %v", err)
	}
	return buf.Bytes()
}

type txn struct {
	req  *http.Request
	resp *http.Response
	body string
}

// runExchange drives request bytes and response bytes through a real assembler
// and returns wantN paired transactions. want gates WantTransaction.
func runExchange(t *testing.T, reqPayload, respPayload string, wantN int, want func(gopacket.Flow) bool) ([]txn, *StreamFactory) {
	t.Helper()

	var (
		mu   sync.Mutex
		got  []txn
		done = make(chan struct{}, 8)
	)

	f := &StreamFactory{
		WantTransaction: want,
		OnTransaction: func(netFlow, transportFlow gopacket.Flow, req *http.Request, resp *http.Response) {
			body, _ := io.ReadAll(resp.Body)
			mu.Lock()
			got = append(got, txn{req: req, resp: resp, body: string(body)})
			mu.Unlock()
			done <- struct{}{}
		},
	}
	asm := tcpassembly.NewAssembler(tcpassembly.NewStreamPool(f))

	feed := func(frame []byte) {
		p := gopacket.NewPacket(frame, layers.LayerTypeEthernet, gopacket.Default)
		tcp, ok := p.TransportLayer().(*layers.TCP)
		if !ok {
			t.Fatalf("no TCP layer")
		}
		asm.AssembleWithTimestamp(p.NetworkLayer().NetworkFlow(), tcp, time.Now())
	}

	feed(dirFrame(t, clientIP, serverIP, clientPort, serverPort, 1, reqPayload))
	if respPayload != "" {
		feed(dirFrame(t, serverIP, clientIP, serverPort, clientPort, 1, respPayload))
	}
	asm.FlushAll()

	// Pairing crosses goroutines; wait for the callbacks rather than sleeping.
	deadline := time.After(5 * time.Second)
	for {
		mu.Lock()
		n := len(got)
		mu.Unlock()
		if n >= wantN {
			break
		}
		select {
		case <-done:
		case <-deadline:
			t.Fatalf("timed out with %d/%d paired transactions", n, wantN)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	return append([]txn(nil), got...), f
}

// The whole reason pairing exists: only the request says whether a response
// carries a body. Handed a HEAD request, ReadResponse must not consume the
// next response's bytes as this one's body.
func TestPairsHeadResponseWithoutBody(t *testing.T) {
	req := "HEAD /thing HTTP/1.1\r\nHost: api.example.com\r\n\r\n"
	resp := "HTTP/1.1 200 OK\r\nContent-Length: 42\r\nContent-Type: text/plain\r\n\r\n"

	got, _ := runExchange(t, req, resp, 1, nil)
	if len(got) != 1 {
		t.Fatalf("got %d transactions, want 1", len(got))
	}
	if got[0].body != "" {
		t.Errorf("HEAD response body = %q, want empty", got[0].body)
	}
	if got[0].resp.StatusCode != 200 {
		t.Errorf("status = %d, want 200", got[0].resp.StatusCode)
	}
}

// 204 likewise has no body regardless of what follows it on the wire.
func TestPairsNoContentResponse(t *testing.T) {
	req := "DELETE /thing HTTP/1.1\r\nHost: api.example.com\r\n\r\n"
	resp := "HTTP/1.1 204 No Content\r\n\r\n"

	got, _ := runExchange(t, req, resp, 1, nil)
	if len(got) != 1 {
		t.Fatalf("got %d transactions, want 1", len(got))
	}
	if got[0].resp.StatusCode != 204 || got[0].body != "" {
		t.Errorf("got status=%d body=%q, want 204 with empty body", got[0].resp.StatusCode, got[0].body)
	}
}

// The query string must survive: Shop's ext_proc keys on Envoy's :path, which
// includes it.
func TestPairedRequestRetainsQueryString(t *testing.T) {
	req := "GET /v1/users?active=true HTTP/1.1\r\nHost: api.example.com\r\n\r\n"
	resp := "HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nhi"

	got, _ := runExchange(t, req, resp, 1, nil)
	if len(got) != 1 {
		t.Fatalf("got %d transactions, want 1", len(got))
	}
	if got[0].req.RequestURI != "/v1/users?active=true" {
		t.Errorf("RequestURI = %q, want /v1/users?active=true", got[0].req.RequestURI)
	}
	if got[0].body != "hi" {
		t.Errorf("body = %q, want hi", got[0].body)
	}
}

// Pipelined requests pair in order — the property FIFO matching relies on.
func TestPairsPipelinedRequestsInOrder(t *testing.T) {
	req := "GET /one HTTP/1.1\r\nHost: a\r\n\r\n" +
		"GET /two HTTP/1.1\r\nHost: a\r\n\r\n" +
		"GET /three HTTP/1.1\r\nHost: a\r\n\r\n"
	resp := "HTTP/1.1 201 Created\r\nContent-Length: 3\r\n\r\none" +
		"HTTP/1.1 202 Accepted\r\nContent-Length: 3\r\n\r\ntwo" +
		"HTTP/1.1 203 Non-Authoritative Information\r\nContent-Length: 3\r\n\r\nthr"

	got, _ := runExchange(t, req, resp, 3, nil)
	if len(got) != 3 {
		t.Fatalf("got %d transactions, want 3", len(got))
	}
	wantURIs := []string{"/one", "/two", "/three"}
	wantStatus := []int{201, 202, 203}
	wantBody := []string{"one", "two", "thr"}
	for i := range got {
		if got[i].req.RequestURI != wantURIs[i] {
			t.Errorf("txn %d URI = %q, want %q", i, got[i].req.RequestURI, wantURIs[i])
		}
		if got[i].resp.StatusCode != wantStatus[i] {
			t.Errorf("txn %d status = %d, want %d", i, got[i].resp.StatusCode, wantStatus[i])
		}
		if got[i].body != wantBody[i] {
			t.Errorf("txn %d body = %q, want %q", i, got[i].body, wantBody[i])
		}
	}
}

// An over-cap response body is dropped, never truncated: a short body replayed
// as a mock produces a green shadow result that tested only the error path.
func TestOversizedResponseBodyIsDroppedNotTruncated(t *testing.T) {
	big := make([]byte, maxEgressBodyBytes+1024)
	for i := range big {
		big[i] = 'x'
	}
	req := "GET /big HTTP/1.1\r\nHost: a\r\n\r\n"
	resp := "HTTP/1.1 200 OK\r\nContent-Length: " +
		itoa(len(big)) + "\r\n\r\n" + string(big)

	f := &StreamFactory{
		OnTransaction: func(gopacket.Flow, gopacket.Flow, *http.Request, *http.Response) {
			t.Error("over-cap response must not be delivered")
		},
	}
	asm := tcpassembly.NewAssembler(tcpassembly.NewStreamPool(f))
	feed := func(frame []byte) {
		p := gopacket.NewPacket(frame, layers.LayerTypeEthernet, gopacket.Default)
		tcp := p.TransportLayer().(*layers.TCP)
		asm.AssembleWithTimestamp(p.NetworkLayer().NetworkFlow(), tcp, time.Now())
	}
	feed(dirFrame(t, clientIP, serverIP, clientPort, serverPort, 1, req))
	feed(dirFrame(t, serverIP, clientIP, serverPort, clientPort, 1, resp))
	asm.FlushAll()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if f.Oversized() > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("oversized counter = %d, want >0", f.Oversized())
}

// The gate must be evaluated on the REQUEST direction for both half-streams.
//
// Regression: WantTransaction was applied to each stream's own flow, so on the
// response half -- which runs dependency->target -- it tested the dependency's
// address, returned false, and discarded the response before it could pair. All
// egress recording silently produced nothing. Earlier tests missed this because
// they left WantTransaction nil, which pairs everything.
func TestPairingGateUsesRequestDirectionForBothHalves(t *testing.T) {
	req := "GET /gated HTTP/1.1\r\nHost: dep\r\n\r\n"
	resp := "HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nhi"

	// Realistic gate: only the client (clientIP) is a capture target, exactly
	// as the exporter's router-backed predicate behaves for egress.
	var asked []string
	want := func(f gopacket.Flow) bool {
		asked = append(asked, f.Src().String())
		return f.Src().String() == clientIP.String()
	}

	got, _ := runExchange(t, req, resp, 1, want)
	if len(got) != 1 {
		t.Fatalf("got %d transactions, want 1 -- the response half was gated out", len(got))
	}
	for _, src := range asked {
		if src == serverIP.String() {
			t.Errorf("gate was asked with the response direction (src=%s); it must be asked with the request direction", src)
		}
	}
}

// WantTransaction false must skip pairing entirely, so ingress-only
// connections never buffer a response body.
func TestWantTransactionFalseSkipsPairing(t *testing.T) {
	req := "GET /skip HTTP/1.1\r\nHost: a\r\n\r\n"
	resp := "HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nhi"

	f := &StreamFactory{
		WantTransaction: func(gopacket.Flow) bool { return false },
		OnTransaction: func(gopacket.Flow, gopacket.Flow, *http.Request, *http.Response) {
			t.Error("pairing must be skipped when WantTransaction is false")
		},
	}
	asm := tcpassembly.NewAssembler(tcpassembly.NewStreamPool(f))
	feed := func(frame []byte) {
		p := gopacket.NewPacket(frame, layers.LayerTypeEthernet, gopacket.Default)
		tcp := p.TransportLayer().(*layers.TCP)
		asm.AssembleWithTimestamp(p.NetworkLayer().NetworkFlow(), tcp, time.Now())
	}
	feed(dirFrame(t, clientIP, serverIP, clientPort, serverPort, 1, req))
	feed(dirFrame(t, serverIP, clientIP, serverPort, clientPort, 1, resp))
	asm.FlushAll()
	time.Sleep(200 * time.Millisecond)
}

// Both directions of one connection must resolve to the same table key, or the
// response could never find its request.
func TestConnKeyIsDirectionIndependent(t *testing.T) {
	netFlow, _ := gopacket.FlowFromEndpoints(
		layers.NewIPEndpoint(clientIP), layers.NewIPEndpoint(serverIP))
	transFlow, _ := gopacket.FlowFromEndpoints(
		layers.NewTCPPortEndpoint(clientPort), layers.NewTCPPortEndpoint(serverPort))

	fwd := connKey(netFlow, transFlow)
	rev := connKey(netFlow.Reverse(), transFlow.Reverse())
	if fwd != rev {
		t.Fatalf("connKey not symmetric:\n  fwd=%s\n  rev=%s", fwd, rev)
	}
}

// Idle connection state must not accumulate for every connection ever seen.
func TestConnTableSweepEvictsIdle(t *testing.T) {
	tbl := newConnTable()
	now := time.Now()
	tbl.now = func() time.Time { return now }

	tbl.get("a|b")
	tbl.get("c|d")
	if len(tbl.conns) != 2 {
		t.Fatalf("got %d conns, want 2", len(tbl.conns))
	}

	now = now.Add(pairSweepAge + time.Second)
	tbl.sweep()
	if len(tbl.conns) != 0 {
		t.Fatalf("got %d conns after sweep, want 0", len(tbl.conns))
	}
}

// A full pending channel must drop rather than block the stream goroutine that
// feeds every other connection.
func TestPublishDropsWhenNoResponseSideDrains(t *testing.T) {
	c := &conn{pending: make(chan *pendingReq, pendingDepth)}
	for i := 0; i < pendingDepth; i++ {
		if !c.publish(&http.Request{}) {
			t.Fatalf("publish %d should have succeeded", i)
		}
	}
	if c.publish(&http.Request{}) {
		t.Fatal("publish past pendingDepth should drop, not block")
	}
}

// await must give up rather than hand ReadResponse a nil request to guess with.
func TestAwaitTimesOutWithoutRequest(t *testing.T) {
	c := &conn{pending: make(chan *pendingReq, pendingDepth)}
	if got := c.await(20 * time.Millisecond); got != nil {
		t.Fatalf("await returned %v, want nil on timeout", got)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
