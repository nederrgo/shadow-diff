//go:build linux && integration

package capture_test

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gopacket/gopacket"

	"github.com/shadow-diff/kaisel/internal/capture"
)

// egressServerPy replies with a body whose length is taken from the request
// path (/size/<n>), so a test can demand a body far larger than one packet.
// It also sets Content-Type, which the export allowlist keeps.
const egressServerPy = `
import socket, sys, threading
host, port = sys.argv[1], int(sys.argv[2])
s = socket.socket(); s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
s.bind((host, port)); s.listen(8)
def handle(c):
    c.settimeout(15)
    try:
        buf = b""
        while b"\r\n\r\n" not in buf:
            d = c.recv(65536)
            if not d: return
            buf += d
        line = buf.split(b"\r\n", 1)[0].decode()
        path = line.split(" ")[1] if " " in line else "/"
        n = 2
        if path.startswith("/size/"):
            n = int(path[len("/size/"):].split("?")[0])
        body = (b"z" * n) if n != 2 else b"ok"
        c.sendall(
            b"HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: "
            + str(len(body)).encode() + b"\r\n\r\n" + body)
    except Exception:
        pass
    finally:
        c.close()
while True:
    try:
        c, _ = s.accept()
    except OSError:
        break
    threading.Thread(target=handle, args=(c,), daemon=True).start()
`

func startEgressServer(t *testing.T, ns, ip string, port int) {
	t.Helper()
	cmd := exec.Command("ip", "netns", "exec", ns, "python3", "-c", egressServerPy, ip, fmt.Sprint(port))
	if err := cmd.Start(); err != nil {
		t.Fatalf("start egress server in %s: %v", ns, err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		probe := exec.Command("ip", "netns", "exec", ns, "python3", "-c",
			fmt.Sprintf("import socket;socket.create_connection((%q,%d),1).close()", ip, port))
		if probe.Run() == nil {
			return
		}
		time.Sleep(150 * time.Millisecond)
	}
	t.Fatalf("egress server %s:%d never came up", ip, port)
}

type egressTxn struct {
	req      *http.Request
	status   int
	respBody []byte
	respHdr  http.Header
}

type txnCollector struct {
	mu   sync.Mutex
	txns []egressTxn
	stop context.CancelFunc
	done chan struct{}
}

func (c *txnCollector) snapshot() []egressTxn {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]egressTxn(nil), c.txns...)
}

func (c *txnCollector) waitFor(n int, timeout time.Duration) []egressTxn {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if txns := c.snapshot(); len(txns) >= n {
			return txns
		}
		time.Sleep(50 * time.Millisecond)
	}
	return c.snapshot()
}

// startEgressCapture attaches to iface and collects paired transactions.
func startEgressCapture(t *testing.T, iface string, targets []string, ports []uint16) *txnCollector {
	t.Helper()

	c := &txnCollector{done: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	c.stop = cancel

	framing, err := capture.DetectFraming(iface)
	if err != nil {
		t.Fatalf("detect framing on %s: %v", iface, err)
	}

	ips := targetIPs(targets, 0)

	// Gate pairing the way the exporter does -- only a target address, and only
	// in the request direction. Leaving this nil would pair everything and hide
	// the class of bug where the response half is gated out by its own flow.
	targetSet := map[string]bool{}
	for _, s := range targets {
		targetSet[s] = true
	}

	ready := make(chan struct{})
	var once sync.Once
	cfg := capture.Config{
		Iface:           iface,
		Targets:         ips,
		Ports:           ports,
		Framing:         framing,
		Ready:           func() { once.Do(func() { close(ready) }) },
		WantTransaction: func(f gopacket.Flow) bool { return targetSet[f.Src().String()] },
		OnTransaction: func(_, _ gopacket.Flow, req *http.Request, resp *http.Response) {
			body, _ := io.ReadAll(resp.Body)
			c.mu.Lock()
			c.txns = append(c.txns, egressTxn{
				req: req, status: resp.StatusCode, respBody: body, respHdr: resp.Header,
			})
			c.mu.Unlock()
		},
	}

	go func() {
		defer close(c.done)
		if err := capture.Run(ctx, cfg); err != nil {
			t.Errorf("capture.Run: %v", err)
		}
	}()

	select {
	case <-ready:
	case <-time.After(15 * time.Second):
		t.Fatal("capture never became ready")
	}
	t.Cleanup(func() {
		c.stop()
		<-c.done
	})
	return c
}

// The target pod acting as a CLIENT: the kernel filter matches on source, and
// user space must pair the outbound request with the inbound response so the
// pair can be recorded as a replayable mock.
func TestCapturesEgressTransaction(t *testing.T) {
	setupLab(t)
	startEgressServer(t, nsB, podBIP, appPort)

	// Capture on pod-a's veth, targeting pod-a: pod-a is the caller here.
	c := startEgressCapture(t, vethA, []string{podAIP}, []uint16{appPort})

	url := appURL(podBIP, appPort, "/v1/items?active=true")
	run(t, "ip", "netns", "exec", nsA, "curl", "-s", "--max-time", "10",
		"-H", "traceparent: 00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
		"-o", "/dev/null", url)

	txns := c.waitFor(1, 15*time.Second)
	if len(txns) == 0 {
		t.Fatal("no egress transaction paired")
	}
	got := txns[0]

	if got.req.Method != http.MethodGet {
		t.Errorf("method = %q, want GET", got.req.Method)
	}
	// The query must survive: Envoy's :path carries it, so the recorded key
	// must too or the mock is stored where nothing looks for it.
	if got.req.RequestURI != "/v1/items?active=true" {
		t.Errorf("RequestURI = %q, want the query retained", got.req.RequestURI)
	}
	if want := fmt.Sprintf("%s:%d", podBIP, appPort); got.req.Host != want {
		t.Errorf("Host = %q, want %q", got.req.Host, want)
	}
	if got.req.Header.Get("traceparent") == "" {
		t.Error("traceparent must survive to the pairing callback; export keys on it")
	}
	if got.status != 200 {
		t.Errorf("status = %d, want 200", got.status)
	}
	if string(got.respBody) != "ok" {
		t.Errorf("response body = %q, want ok", string(got.respBody))
	}
	if ct := got.respHdr.Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Errorf("Content-Type = %q, want it captured off the wire", ct)
	}
}

// A mock replayed with a short body is worse than no mock: all three shadow
// roles get identical malformed input and the diff comes back clean. The
// response body must arrive byte-complete across many packets.
func TestEgressResponseBodyIntact(t *testing.T) {
	setupLab(t)
	startEgressServer(t, nsB, podBIP, appPort)

	c := startEgressCapture(t, vethA, []string{podAIP}, []uint16{appPort})

	const size = 200 * 1024
	url := appURL(podBIP, appPort, fmt.Sprintf("/size/%d", size))
	run(t, "ip", "netns", "exec", nsA, "curl", "-s", "--max-time", "20",
		"-H", "traceparent: 00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
		"-o", "/dev/null", url)

	txns := c.waitFor(1, 20*time.Second)
	if len(txns) == 0 {
		t.Fatal("no egress transaction paired")
	}
	body := txns[0].respBody
	if len(body) != size {
		t.Fatalf("response body = %d bytes, want %d", len(body), size)
	}

	want := make([]byte, size)
	for i := range want {
		want[i] = 'z'
	}
	if sha256.Sum256(body) != sha256.Sum256(want) {
		t.Error("response body arrived corrupted; a truncated mock would report a false green")
	}
}
