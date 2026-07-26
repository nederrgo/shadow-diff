package proxy

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"net"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgproto3"
	"github.com/shadow-diff/shadow-soldier/internal/parsers"
)

// --- Postgres opportunistic SSL handshake ---
//
// These are the highest-value tests in the package. If the 'N' reply is missing
// or the probe is forwarded upstream, every pgx client hangs or fails to connect
// while every other test in this repo still passes.

// fakeRW plays the client side of a handshake: it serves canned bytes and
// records whatever the proxy wrote back.
type fakeRW struct {
	in  *bytes.Reader
	out bytes.Buffer
}

func (f *fakeRW) Read(p []byte) (int, error)  { return f.in.Read(p) }
func (f *fakeRW) Write(p []byte) (int, error) { return f.out.Write(p) }

func sslRequest(code uint32) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint32(b[0:4], 8)
	binary.BigEndian.PutUint32(b[4:8], code)
	return b
}

func TestNegotiatePostgresPlaintextAnswersSSLRequest(t *testing.T) {
	t.Parallel()
	startup, err := (&pgproto3.StartupMessage{
		ProtocolVersion: pgproto3.ProtocolVersionNumber,
		Parameters:      map[string]string{"user": "u"},
	}).Encode(nil)
	if err != nil {
		t.Fatalf("encode startup: %v", err)
	}

	f := &fakeRW{in: bytes.NewReader(append(sslRequest(sslRequestCode), startup...))}
	upstream, err := negotiatePostgresPlaintext(f)
	if err != nil {
		t.Fatalf("negotiate: %v", err)
	}

	// Exactly one byte, 'N', goes back to the client.
	if got := f.out.Bytes(); len(got) != 1 || got[0] != 'N' {
		t.Fatalf("client reply = %q want single 'N'", got)
	}

	// The 8-byte probe must NOT be forwarded: what goes upstream is the
	// StartupMessage alone, so the real Postgres sees an ordinary connection.
	forwarded, err := io.ReadAll(upstream)
	if err != nil {
		t.Fatalf("read forwarded: %v", err)
	}
	if !bytes.Equal(forwarded, startup) {
		t.Fatalf("forwarded %d bytes, want the %d-byte StartupMessage only", len(forwarded), len(startup))
	}
}

func TestNegotiatePostgresPlaintextAnswersGSSEncRequest(t *testing.T) {
	t.Parallel()
	f := &fakeRW{in: bytes.NewReader(sslRequest(gssEncRequestCode))}
	if _, err := negotiatePostgresPlaintext(f); err != nil {
		t.Fatalf("negotiate: %v", err)
	}
	if got := f.out.Bytes(); len(got) != 1 || got[0] != 'N' {
		t.Fatalf("client reply = %q want single 'N'", got)
	}
}

// A client that never asks for encryption must have its first 8 bytes forwarded
// verbatim — they are the head of the StartupMessage, not a probe.
func TestNegotiatePostgresPlaintextPassesThroughPlainStartup(t *testing.T) {
	t.Parallel()
	startup, err := (&pgproto3.StartupMessage{
		ProtocolVersion: pgproto3.ProtocolVersionNumber,
		Parameters:      map[string]string{"user": "u"},
	}).Encode(nil)
	if err != nil {
		t.Fatalf("encode startup: %v", err)
	}

	f := &fakeRW{in: bytes.NewReader(startup)}
	upstream, err := negotiatePostgresPlaintext(f)
	if err != nil {
		t.Fatalf("negotiate: %v", err)
	}
	if f.out.Len() != 0 {
		t.Fatalf("wrote %q to client, want nothing", f.out.Bytes())
	}
	forwarded, err := io.ReadAll(upstream)
	if err != nil {
		t.Fatalf("read forwarded: %v", err)
	}
	if !bytes.Equal(forwarded, startup) {
		t.Fatal("plain StartupMessage was not forwarded byte-for-byte")
	}
}

func TestNegotiatePostgresPlaintextShortRead(t *testing.T) {
	t.Parallel()
	f := &fakeRW{in: bytes.NewReader([]byte{0, 0, 0})}
	if _, err := negotiatePostgresPlaintext(f); err == nil {
		t.Fatal("negotiate = nil error on a truncated probe, want error")
	}
}

// --- tap ---

func TestTapRoundTrip(t *testing.T) {
	t.Parallel()
	tp := newTap(4)
	if _, err := tp.Write([]byte("hello ")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := tp.Write([]byte("world")); err != nil {
		t.Fatalf("write: %v", err)
	}
	tp.close()
	got, err := io.ReadAll(tp)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != "hello world" {
		t.Fatalf("tap = %q want %q", got, "hello world")
	}
}

// The tap must copy: the proxy hands it a pooled buffer that is reused as soon as
// Write returns. Without the copy, every queued chunk would alias one buffer and
// the parser would read the last chunk N times.
func TestTapCopiesCallerBuffer(t *testing.T) {
	t.Parallel()
	tp := newTap(4)
	buf := []byte("first")
	if _, err := tp.Write(buf); err != nil {
		t.Fatalf("write: %v", err)
	}
	copy(buf, "SECON")
	tp.close()
	got, _ := io.ReadAll(tp)
	if string(got) != "first" {
		t.Fatalf("tap = %q want %q — caller buffer was aliased", got, "first")
	}
}

// Overflow must never block the writer, and must end the stream rather than
// deliver a gap the parser would misframe.
func TestTapOverflowDoesNotBlockAndClosesStream(t *testing.T) {
	t.Parallel()
	tp := newTap(2)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 100 {
			if _, err := tp.Write([]byte("xxxx")); err != nil {
				t.Errorf("write: %v", err)
				return
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("tap.Write blocked — the application's query path would stall")
	}
	if !tp.Overflowed() {
		t.Fatal("Overflowed = false after exceeding capacity")
	}
	if _, err := io.ReadAll(tp); err != nil {
		t.Fatalf("read after overflow: %v", err)
	}
}

// --- end-to-end proxying ---

// echoServer stands in for a database: it returns whatever it is sent.
func echoServer(t *testing.T) (addr string, received func() []byte) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	var mu sync.Mutex
	var got []byte
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				buf := make([]byte, 4096)
				for {
					n, err := c.Read(buf)
					if n > 0 {
						mu.Lock()
						got = append(got, buf[:n]...)
						mu.Unlock()
						_, _ = c.Write(buf[:n])
					}
					if err != nil {
						return
					}
				}
			}(c)
		}
	}()
	return ln.Addr().String(), func() []byte {
		mu.Lock()
		defer mu.Unlock()
		return append([]byte(nil), got...)
	}
}

// startProxy runs a Server on an ephemeral port and returns its address.
func startProxy(t *testing.T, protocol, upstream string, emit func(parsers.QueryReport)) string {
	t.Helper()
	// Bind port 0 to get a free port, then hand that port to the Server.
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("probe listen: %v", err)
	}
	port := probe.Addr().(*net.TCPAddr).Port
	_ = probe.Close()

	ctx, cancel := context.WithCancel(context.Background())
	srv := &Server{
		Routes: []Route{{Protocol: protocol, Listen: port, Upstream: upstream}},
		Emit:   emit,
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := srv.Run(ctx); err != nil {
			t.Errorf("Run: %v", err)
		}
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})

	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	waitDialable(t, addr)
	return addr
}

func waitDialable(t *testing.T, addr string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		c, err := net.Dial("tcp", addr)
		if err == nil {
			_ = c.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("proxy at %s never became dialable", addr)
}

func TestProxyForwardsBytesAndParses(t *testing.T) {
	t.Parallel()
	upstream, received := echoServer(t)

	var mu sync.Mutex
	var reports []parsers.QueryReport
	addr := startProxy(t, parsers.ProtocolRedis, upstream, func(q parsers.QueryReport) {
		mu.Lock()
		reports = append(reports, q)
		mu.Unlock()
	})

	client, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial proxy: %v", err)
	}
	defer client.Close()

	cmd := "*2\r\n$3\r\nGET\r\n$5\r\nmykey\r\n"
	if _, err := client.Write([]byte(cmd)); err != nil {
		t.Fatalf("write: %v", err)
	}

	// The echo proves the bytes reached the upstream unmodified and came back.
	echo := make([]byte, len(cmd))
	_ = client.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := io.ReadFull(client, echo); err != nil {
		t.Fatalf("read echo: %v", err)
	}
	if string(echo) != cmd {
		t.Fatalf("echo = %q want %q", echo, cmd)
	}
	if got := string(received()); got != cmd {
		t.Fatalf("upstream received %q want %q", got, cmd)
	}

	waitFor(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(reports) == 1
	}, "parser never emitted a report")

	mu.Lock()
	defer mu.Unlock()
	if got := reports[0].Signature(); got != "redis:get:mykey" {
		t.Fatalf("Signature = %q want redis:get:mykey", got)
	}
}

// A protocol whose bytes the parser cannot make sense of must still be proxied
// byte-for-byte. This is the fail-open guarantee.
func TestProxyStaysOpenWhenParserFails(t *testing.T) {
	t.Parallel()
	upstream, received := echoServer(t)
	addr := startProxy(t, parsers.ProtocolMongoDB, upstream, func(parsers.QueryReport) {})

	client, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial proxy: %v", err)
	}
	defer client.Close()

	garbage := []byte("this is not a mongodb wire frame at all, not even close")
	if _, err := client.Write(garbage); err != nil {
		t.Fatalf("write: %v", err)
	}
	echo := make([]byte, len(garbage))
	_ = client.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := io.ReadFull(client, echo); err != nil {
		t.Fatalf("read echo: %v", err)
	}
	if !bytes.Equal(echo, garbage) {
		t.Fatal("unparseable bytes were not forwarded verbatim")
	}
	if !bytes.Equal(received(), garbage) {
		t.Fatal("upstream did not receive the unparseable bytes")
	}
}

func TestRouteValidate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		route   Route
		wantErr bool
	}{
		{Route{Protocol: parsers.ProtocolMongoDB, Listen: 27017, Upstream: "mongo:27017"}, false},
		{Route{Protocol: "cassandra", Listen: 9042, Upstream: "c:9042"}, true},
		{Route{Protocol: parsers.ProtocolRedis, Listen: 0, Upstream: "r:6379"}, true},
		{Route{Protocol: parsers.ProtocolRedis, Listen: 70000, Upstream: "r:6379"}, true},
		{Route{Protocol: parsers.ProtocolRedis, Listen: 6379, Upstream: "  "}, true},
	}
	for _, tc := range tests {
		err := tc.route.Validate()
		if (err != nil) != tc.wantErr {
			t.Fatalf("Validate(%+v) = %v, wantErr %v", tc.route, err, tc.wantErr)
		}
	}
}

func waitFor(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal(msg)
}
