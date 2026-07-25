//go:build linux && integration

package capture_test

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// settleWindow is how long a negative test waits before concluding nothing was
// captured. Long enough that a filter regression would have produced a record.
const settleWindow = 4 * time.Second

// A pod inside the cluster calls the monitored pod. The filter matches on
// destination, so the peer's identity is irrelevant -- this and
// TestCapturesExternalSource are the same target and capture point with
// radically different peers, and both must land.
func TestCapturesInClusterSource(t *testing.T) {
	setupLab(t)
	startServer(t, nsA, podAIP, appPort)

	c := startCapture(t, vethA, []string{podAIP}, []uint16{appPort})
	curlFromNS(t, nsB, appURL(podAIP, appPort, "/in-cluster"))

	reqs, _ := c.waitFor(1, 10*time.Second)
	if len(reqs) == 0 {
		t.Fatal("no request captured from an in-cluster peer")
	}
	if reqs[0].RequestURI != "/in-cluster" {
		t.Errorf("RequestURI = %q, want /in-cluster", reqs[0].RequestURI)
	}
	if reqs[0].Host != fmt.Sprintf("%s:%d", podAIP, appPort) {
		t.Errorf("Host = %q", reqs[0].Host)
	}
}

// A client outside the cluster reaches the monitored pod. Same target, same
// capture point, off-cluster source address.
func TestCapturesExternalSource(t *testing.T) {
	setupLab(t)
	startServer(t, nsA, podAIP, appPort)

	c := startCapture(t, vethA, []string{podAIP}, []uint16{appPort})
	curlFromHost(t, extIP, appURL(podAIP, appPort, "/from-outside"))

	reqs, _ := c.waitFor(1, 10*time.Second)
	if len(reqs) == 0 {
		t.Fatal("no request captured from an external client")
	}
	if reqs[0].RequestURI != "/from-outside" {
		t.Errorf("RequestURI = %q, want /from-outside", reqs[0].RequestURI)
	}
}

// Watching pod-a must not capture pod-b's traffic. Capture runs on pod-b's own
// veth, so the traffic is unquestionably on the wire being sniffed -- only the
// address filter stands between it and a record.
func TestIgnoresUnrelatedPods(t *testing.T) {
	setupLab(t)
	startServer(t, nsB, podBIP, appPort)

	c := startCapture(t, vethB, []string{podAIP}, []uint16{appPort})
	curlFromHost(t, hostIP, appURL(podBIP, appPort, "/not-yours"))

	reqs, _ := c.waitFor(1, settleWindow)
	if len(reqs) != 0 {
		t.Fatalf("captured %d requests for a pod we were not asked to watch: %q",
			len(reqs), reqs[0].RequestURI)
	}
}

// Traffic to the monitored pod on a port outside targetPorts must be dropped in
// the kernel. Since sampling cannot protect the perf ring, this filter is one
// of the few levers that reduces what crosses into user space at all.
func TestIgnoresNonTargetPort(t *testing.T) {
	setupLab(t)
	startServer(t, nsA, podAIP, appPort)
	startServer(t, nsA, podAIP, altPort)

	c := startCapture(t, vethA, []string{podAIP}, []uint16{appPort})
	curlFromNS(t, nsB, appURL(podAIP, altPort, "/wrong-port"))

	if reqs, _ := c.waitFor(1, settleWindow); len(reqs) != 0 {
		t.Fatalf("captured %d requests on a non-target port: %q", len(reqs), reqs[0].RequestURI)
	}

	// The same capture must still see the port it was asked for -- proving the
	// silence above was the port filter, not a broken capture.
	curlFromNS(t, nsB, appURL(podAIP, appPort, "/right-port"))
	reqs, _ := c.waitFor(1, 10*time.Second)
	if len(reqs) == 0 {
		t.Fatal("target port went uncaptured; the negative result above proves nothing")
	}
	if reqs[0].RequestURI != "/right-port" {
		t.Errorf("RequestURI = %q, want /right-port", reqs[0].RequestURI)
	}
}

// The regression test for the whole change. GSO hands AF_PACKET packets far
// larger than the MTU -- 62,557 bytes measured on this host -- and the old
// 2048-byte clamp discarded ~97% of them. A truncated body is worthless:
// replayed to all three shadow roles it produces identical failures, a clean
// diff, and a green test that exercised nothing.
func TestLargeRequestBodyIntact(t *testing.T) {
	setupLab(t)
	startServer(t, nsA, podAIP, appPort)

	c := startCapture(t, vethA, []string{podAIP}, []uint16{appPort})

	const bodySize = 200000
	payload := bytes.Repeat([]byte("x"), bodySize)
	body := append([]byte(`{"d":"`), append(payload, []byte(`"}`)...)...)
	want := sha256.Sum256(body)

	postFromNS(t, nsB, appURL(podAIP, appPort, "/upload"), body)

	reqs, bodies := c.waitFor(1, 20*time.Second)
	if len(reqs) == 0 {
		t.Fatal("large POST was not captured at all")
	}
	got := sha256.Sum256(bodies[0])
	if len(bodies[0]) != len(body) {
		t.Fatalf("body truncated: got %d bytes, want %d (%.1f%% lost)",
			len(bodies[0]), len(body),
			100*(1-float64(len(bodies[0]))/float64(len(body))))
	}
	if got != want {
		t.Errorf("body corrupted: sha256 mismatch despite correct length")
	}
}

// Encrypted traffic to a monitored port must yield no records rather than
// garbage parsed out of ciphertext.
func TestTLSNotMisparsed(t *testing.T) {
	setupLab(t)
	startTLSServer(t, nsA, podAIP, appPort)

	c := startCapture(t, vethA, []string{podAIP}, []uint16{appPort})
	// -k because the cert is self-signed; the handshake is what matters.
	_ = exec.Command("ip", "netns", "exec", nsB, "curl", "-sk", "--max-time", "10",
		"-o", "/dev/null", fmt.Sprintf("https://%s:%d/secret", podAIP, appPort)).Run()

	reqs, _ := c.waitFor(1, settleWindow)
	if len(reqs) != 0 {
		var uris []string
		for _, r := range reqs {
			uris = append(uris, r.RequestURI)
		}
		t.Fatalf("parsed %d HTTP requests out of TLS traffic: %s",
			len(reqs), strings.Join(uris, ", "))
	}
}

func postFromNS(t *testing.T, ns, url string, body []byte) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "body.json")
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatalf("write body file: %v", err)
	}
	run(t, "ip", "netns", "exec", ns, "curl", "-s", "--max-time", "20",
		"-X", "POST", "--data-binary", "@"+path, "-o", "/dev/null", url)
}
