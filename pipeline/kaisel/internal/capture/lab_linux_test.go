//go:build linux && integration

package capture_test

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gopacket/gopacket"

	"github.com/shadow-diff/kaisel/internal/capture"
)

// A simulated node: two pods on a bridge, plus an off-cluster client address.
//
//	       kbr0 (bridge) 10.99.0.1/24  + 203.0.113.5/32 "external"
//	        │                     │
//	 kaveth (host side)     kbveth (host side)   <-- capture points
//	        │                     │
//	netns kpod-a            netns kpod-b
//	 10.99.0.2               10.99.0.3
//
// Capture attaches to a pod's HOST-SIDE VETH, not the bridge: a Linux bridge
// device does not deliver frames it forwards between ports to AF_PACKET
// listeners, so capturing on kbr0 sees nothing of pod-to-pod traffic. Every
// frame to or from a pod does traverse that pod's veth, in both directions.
const (
	brName  = "kbr0"
	nsA     = "kpod-a"
	nsB     = "kpod-b"
	vethA   = "kaveth" // host side, pod-a
	vethB   = "kbveth" // host side, pod-b
	peerA   = "kapeer" // moved into nsA
	peerB   = "kbpeer" // moved into nsB
	hostIP  = "10.99.0.1"
	podAIP  = "10.99.0.2"
	podBIP  = "10.99.0.3"
	extIP   = "203.0.113.5" // off-cluster client source
	appPort = 8080
	altPort = 9090
)

func run(t *testing.T, name string, args ...string) {
	t.Helper()
	if out, err := exec.Command(name, args...).CombinedOutput(); err != nil {
		t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out)
	}
}

func quiet(name string, args ...string) { _ = exec.Command(name, args...).Run() }

// teardown removes everything the lab creates. Also run before setup so a
// crashed previous run cannot poison this one.
func teardown() {
	quiet("ip", "netns", "del", nsA)
	quiet("ip", "netns", "del", nsB)
	quiet("ip", "link", "del", vethA)
	quiet("ip", "link", "del", vethB)
	quiet("ip", "link", "del", brName)
}

func setupLab(t *testing.T) {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Skip("integration tests need root for CAP_BPF and CAP_NET_RAW")
	}
	teardown()
	t.Cleanup(teardown)

	run(t, "ip", "link", "add", brName, "type", "bridge")
	run(t, "ip", "addr", "add", hostIP+"/24", "dev", brName)
	run(t, "ip", "addr", "add", extIP+"/32", "dev", brName)
	run(t, "ip", "link", "set", brName, "up")

	for _, p := range []struct{ ns, host, peer, ip string }{
		{nsA, vethA, peerA, podAIP},
		{nsB, vethB, peerB, podBIP},
	} {
		run(t, "ip", "netns", "add", p.ns)
		run(t, "ip", "link", "add", p.host, "type", "veth", "peer", "name", p.peer)
		run(t, "ip", "link", "set", p.host, "master", brName)
		run(t, "ip", "link", "set", p.host, "up")
		run(t, "ip", "link", "set", p.peer, "netns", p.ns)
		run(t, "ip", "netns", "exec", p.ns, "ip", "addr", "add", p.ip+"/24", "dev", p.peer)
		run(t, "ip", "netns", "exec", p.ns, "ip", "link", "set", p.peer, "up")
		run(t, "ip", "netns", "exec", p.ns, "ip", "link", "set", "lo", "up")
		// Route back to the off-cluster client address.
		run(t, "ip", "netns", "exec", p.ns, "ip", "route", "add", "default", "via", hostIP)
	}
}

// serverPy accepts HTTP, reads the whole body, and replies. Kept minimal
// because it only has to terminate the connection realistically.
const serverPy = `
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
        head, rest = buf.split(b"\r\n\r\n", 1)
        n = 0
        for line in head.split(b"\r\n"):
            if line.lower().startswith(b"content-length:"):
                n = int(line.split(b":")[1])
        got = len(rest)
        while got < n:
            d = c.recv(65536)
            if not d: break
            got += len(d)
        c.sendall(b"HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nok")
    except Exception:
        pass
    finally:
        c.close()
while True:
    try:
        c, _ = s.accept()
        threading.Thread(target=handle, args=(c,), daemon=True).start()
    except Exception:
        break
`

// startServer runs an HTTP server inside a netns and waits for it to listen.
func startServer(t *testing.T, ns, ip string, port int) {
	t.Helper()
	cmd := exec.Command("ip", "netns", "exec", ns, "python3", "-c", serverPy, ip, fmt.Sprint(port))
	if err := cmd.Start(); err != nil {
		t.Fatalf("start server in %s: %v", ns, err)
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
	t.Fatalf("server %s:%d never came up", ip, port)
}

// collector runs capture.Run and records every parsed request.
type collector struct {
	mu     sync.Mutex
	reqs   []*http.Request
	bodies [][]byte
	stop   context.CancelFunc
	done   chan struct{}
}

func (c *collector) snapshot() ([]*http.Request, [][]byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]*http.Request(nil), c.reqs...), append([][]byte(nil), c.bodies...)
}

// waitFor polls until n requests arrive or the timeout expires. Returns what it
// has either way, so negative tests can assert emptiness after a full wait.
func (c *collector) waitFor(n int, timeout time.Duration) ([]*http.Request, [][]byte) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if reqs, bodies := c.snapshot(); len(reqs) >= n {
			return reqs, bodies
		}
		time.Sleep(50 * time.Millisecond)
	}
	return c.snapshot()
}

// startCapture attaches to iface and blocks until the filter is live.
func startCapture(t *testing.T, iface string, targets []string, ports []uint16) *collector {
	t.Helper()

	c := &collector{done: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	c.stop = cancel

	framing, err := capture.DetectFraming(iface)
	if err != nil {
		t.Fatalf("detect framing on %s: %v", iface, err)
	}

	var ips []net.IP
	for _, s := range targets {
		ips = append(ips, net.ParseIP(s))
	}

	ready := make(chan struct{})
	var once sync.Once
	cfg := capture.Config{
		Iface:   iface,
		Targets: ips,
		Ports:   ports,
		Framing: framing,
		Ready:   func() { once.Do(func() { close(ready) }) },
		OnRequest: func(_, _ gopacket.Flow, req *http.Request) {
			// Read the body here: OnRequest runs before the drain precisely so
			// callers can, and the large-body test depends on it.
			body, _ := io.ReadAll(req.Body)
			c.mu.Lock()
			c.reqs = append(c.reqs, req)
			c.bodies = append(c.bodies, body)
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
		cancel()
		select {
		case <-c.done:
		case <-time.After(10 * time.Second):
			t.Error("capture did not shut down")
		}
	})
	return c
}

// tlsServerPy wraps the same accept loop in TLS.
const tlsServerPy = `
import socket, ssl, sys, threading
host, port, cert, key = sys.argv[1], int(sys.argv[2]), sys.argv[3], sys.argv[4]
ctx = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
ctx.load_cert_chain(cert, key)
s = socket.socket(); s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
s.bind((host, port)); s.listen(8)
def handle(c):
    try:
        c.settimeout(10)
        c.recv(65536)
        c.sendall(b"HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nok")
    except Exception:
        pass
    finally:
        try: c.close()
        except Exception: pass
while True:
    try:
        raw, _ = s.accept()
        try:
            c = ctx.wrap_socket(raw, server_side=True)
        except Exception:
            raw.close(); continue
        threading.Thread(target=handle, args=(c,), daemon=True).start()
    except Exception:
        break
`

// startTLSServer runs an HTTPS server in a netns with a throwaway self-signed
// certificate.
func startTLSServer(t *testing.T, ns, ip string, port int) {
	t.Helper()
	dir := t.TempDir()
	cert := filepath.Join(dir, "c.pem")
	key := filepath.Join(dir, "k.pem")
	run(t, "openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes",
		"-keyout", key, "-out", cert, "-days", "1", "-subj", "/CN="+ip)

	cmd := exec.Command("ip", "netns", "exec", ns, "python3", "-c", tlsServerPy,
		ip, fmt.Sprint(port), cert, key)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start TLS server in %s: %v", ns, err)
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
	t.Fatalf("TLS server %s:%d never came up", ip, port)
}

// curlFromNS issues a request from inside a netns.
func curlFromNS(t *testing.T, ns, url string) {
	t.Helper()
	run(t, "ip", "netns", "exec", ns, "curl", "-s", "--max-time", "10", "-o", "/dev/null", url)
}

// curlFromHost issues a request from the host, bound to a specific source IP.
func curlFromHost(t *testing.T, srcIP, url string) {
	t.Helper()
	run(t, "curl", "-s", "--max-time", "10", "--interface", srcIP, "-o", "/dev/null", url)
}

func appURL(ip string, port int, path string) string {
	return fmt.Sprintf("http://%s:%d%s", ip, port, path)
}
