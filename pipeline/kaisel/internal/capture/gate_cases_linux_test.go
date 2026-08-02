//go:build linux && integration

package capture

import "testing"

// req builds a client->server request segment on a fixed 5-tuple.
func req(t *testing.T, sport uint16, payload []byte) []byte {
	return seg{
		srcIP: gateSrcIP, dstIP: gateDstIP,
		sport: sport, dport: 8080,
		flags: tcpACK, payload: payload,
	}.build(t)
}

// resp builds the matching server->client segment: reversed 5-tuple, which the
// canonical flow key must fold onto the request's entry.
func resp(t *testing.T, sport uint16, payload []byte) []byte {
	return seg{
		srcIP: gateDstIP, dstIP: gateSrcIP,
		sport: 8080, dport: sport,
		flags: tcpACK, payload: payload,
	}.build(t)
}

// The core of the gate: a trace id that buckets out is dropped in kernel, one
// that buckets in is not. These two vectors straddle the 10% boundary.
func TestGateDropsUnsampledTraceID(t *testing.T) {
	g := newGate(t, gatePct)

	if !g.run(req(t, 40001, httpHead("GET", goldenDropLow, 0))) {
		t.Error("V=26 at 10% must be dropped in kernel")
	}
	if g.admitted(40001, 8080) {
		t.Error("a dropped request must not admit its connection")
	}

	if g.run(req(t, 40002, httpHead("GET", goldenKeepLow, 0))) {
		t.Error("V=0 at 10% must pass")
	}
	if !g.admitted(40002, 8080) {
		t.Error("a sampled-in request must admit its connection")
	}
}

// The boundary case, and the uppercase one. sample.SampledIn lowercases before
// decoding; a kernel that only accepted lowercase would drop what user space
// keeps.
func TestGateMatchesGoldenVectors(t *testing.T) {
	g := newGate(t, gatePct)

	if g.run(req(t, 40010, httpHead("GET", goldenKeepHigh, 0))) {
		t.Error("V=25 is the last kept bucket at 10%; must pass")
	}
	if !g.run(req(t, 40011, httpHead("GET", goldenDropUp, 0))) {
		t.Error("uppercase hex must decode and drop at 10%")
	}
}

// The fail-open invariant. Anything the gate cannot decide has to pass, or it
// would drop traffic user space would have kept.
func TestGateFailsOpen(t *testing.T) {
	g := newGate(t, gatePct)

	t.Run("no traceparent", func(t *testing.T) {
		if g.run(req(t, 40020, httpHead("GET", "", 0))) {
			t.Error("an untraced request head must fail open")
		}
	})

	t.Run("traceparent past the window", func(t *testing.T) {
		// Pushed beyond HEADER_WINDOW (768), so the scan never sees it.
		if g.run(req(t, 40021, httpHead("GET", goldenDropLow, 900))) {
			t.Error("a traceparent past the scan window must fail open")
		}
	})

	t.Run("malformed traceparent", func(t *testing.T) {
		bad := []byte("GET / HTTP/1.1\r\nHost: x\r\ntraceparent: garbage\r\n\r\n")
		if g.run(req(t, 40022, bad)) {
			t.Error("an unparseable traceparent must fail open")
		}
	})

	t.Run("head too short to load a tier", func(t *testing.T) {
		// Under HEADER_MIN: nothing is examined, so nothing can be concluded.
		if g.run(req(t, 40023, []byte("GET / HTTP/1.1\r\n\r\n"))) {
			t.Error("a head too short to scan must fail open, not fall through to the drop")
		}
	})
}

// Deep into the window, well past what a single small tier would cover. This
// is the case the 768-byte window exists for.
func TestGateScansDeepIntoWindow(t *testing.T) {
	g := newGate(t, gatePct)
	if !g.run(req(t, 40030, httpHead("GET", goldenDropLow, 400))) {
		t.Error("a traceparent ~400 bytes in must be found and gated")
	}
}

// A tier only ever sits at or below the payload length, so the bytes between
// them are unread by the first load -- and a traceparent, being the last
// header, lands there constantly. The tail-aligned second read is what closes
// that gap. Without it the most ordinary request on the wire fails open.
func TestGateCoversTheTierGap(t *testing.T) {
	g := newGate(t, gatePct)

	// Payload lengths chosen to sit just above a tier, so the trace id runs
	// past what the head-aligned read covers: 685 (tier 640), 158 (tier 128,
	// the size of a plain curl request), 300 (tier 256).
	for _, padTo := range []int{600, 60, 220} {
		payload := httpHead("GET", goldenDropLow, padTo)
		sport := uint16(40200 + padTo)
		if !g.run(req(t, sport, payload)) {
			t.Errorf("payload of %d bytes: trace id past the head tier must still be gated",
				len(payload))
		}
	}
}

// Sampling off is the default for every rule that sets no percentage, and it
// must skip the gate entirely -- this is what keeps the untraced integration
// lab working.
func TestGateOffPassesEverything(t *testing.T) {
	g := newGate(t, 100)
	if g.run(req(t, 40040, httpHead("GET", goldenDropLow, 0))) {
		t.Error("at 100% nothing may be gated out")
	}
}

// A response has no request line, so it can only be judged by whether its
// connection was admitted. This is what the LRU exists for.
func TestGateResponseFollowsItsRequest(t *testing.T) {
	g := newGate(t, gatePct)

	// Admitted connection: the response half must follow it through.
	g.run(req(t, 40050, httpHead("GET", goldenKeepLow, 0)))
	if g.run(resp(t, 40050, httpResponse())) {
		t.Error("the response half of a sampled-in request must pass")
	}

	// Never-seen connection: nothing to follow.
	if !g.run(resp(t, 40051, httpResponse())) {
		t.Error("a response on an unknown 5-tuple must be dropped")
	}
}

// Continuation segments carry no request line either, and must ride on the
// decision their head already made.
func TestGateContinuationFollowsItsHead(t *testing.T) {
	g := newGate(t, gatePct)

	g.run(req(t, 40060, httpHead("POST", goldenKeepLow, 0)))
	if g.run(req(t, 40060, []byte("{\"body\":\"continuation bytes with no request line\"}"))) {
		t.Error("a continuation of a sampled-in request must pass")
	}

	g.run(req(t, 40061, httpHead("POST", goldenDropLow, 0)))
	if !g.run(req(t, 40061, []byte("{\"body\":\"continuation bytes with no request line\"}"))) {
		t.Error("a continuation of a dropped request must be dropped")
	}
}

// Keep-alive: the second request on a connection is judged on its own trace id,
// not on whatever the first one got. Without this, sampling ratios collapse on
// pooled connections.
func TestGateReEvaluatesEachRequestHead(t *testing.T) {
	g := newGate(t, gatePct)

	if g.run(req(t, 40070, httpHead("GET", goldenKeepLow, 0))) {
		t.Fatal("first request should pass")
	}
	if !g.admitted(40070, 8080) {
		t.Fatal("first request should admit the connection")
	}

	if !g.run(req(t, 40070, httpHead("GET", goldenDropLow, 0))) {
		t.Error("a second, unsampled head on the same connection must be dropped")
	}
	if g.admitted(40070, 8080) {
		t.Error("dropping a head must clear the connection, or its response rides through")
	}
}

// A SYN means a new connection under that 5-tuple. Without invalidation, a
// recycled ephemeral port behind SNAT would inherit the previous connection's
// admission.
func TestGateSYNInvalidatesRecycledPort(t *testing.T) {
	g := newGate(t, gatePct)

	g.run(req(t, 40080, httpHead("GET", goldenKeepLow, 0)))
	if !g.admitted(40080, 8080) {
		t.Fatal("expected the connection to be admitted")
	}

	synFrame := seg{
		srcIP: gateSrcIP, dstIP: gateDstIP,
		sport: 40080, dport: 8080, flags: tcpSYN,
	}.build(t)
	if g.run(synFrame) {
		t.Error("a SYN must pass; the assembler needs it")
	}
	if g.admitted(40080, 8080) {
		t.Error("a SYN must clear stale admission for its 5-tuple")
	}

	// And the stale entry really is gone: a response now has nothing to ride.
	if !g.run(resp(t, 40080, httpResponse())) {
		t.Error("after SYN invalidation a response must no longer be admitted")
	}
}

// Payload-free segments. Pure ACKs are pure overhead; FIN and RST are not,
// because decode frames a `Connection: close` body by reading to EOF.
func TestGateZeroPayloadSegments(t *testing.T) {
	g := newGate(t, gatePct)

	bare := func(flags byte) []byte {
		return seg{
			srcIP: gateSrcIP, dstIP: gateDstIP,
			sport: 40090, dport: 8080, flags: flags,
		}.build(t)
	}

	if !g.run(bare(tcpACK)) {
		t.Error("a pure ACK carries nothing and must be dropped")
	}
	for name, f := range map[string]byte{"FIN": tcpFIN, "RST": tcpRST} {
		if g.run(bare(tcpACK | f)) {
			t.Errorf("a payload-free %s must pass; the stream close depends on it", name)
		}
	}
}

// Binary payloads that happen to start with method-like bytes. The trailing
// space in the method token is the only thing separating these from a real
// request head.
func TestGateBinaryPayloadIsNotARequestHead(t *testing.T) {
	g := newGate(t, gatePct)

	// "GET" with no delimiter, then binary. Not a head, and its connection was
	// never admitted, so it drops. Long enough to clear HEADER_MIN, or it would
	// fail open for being unscannable rather than for not being a head.
	binary := []byte("GET\x00\x01\x02\x03 not really http \xff\xfe\x00\x11\x22\x33" +
		"\x44\x55\x66\x77\x88\x99\xaa\xbb\xcc\xdd\xee\xff padding to clear the tier")
	if !g.run(req(t, 40100, binary)) {
		t.Error("a method token without its trailing space must not count as a request head")
	}
}

// TCP options move the payload; a data offset read wrong points the scan at
// header bytes.
func TestGateHandlesTCPOptions(t *testing.T) {
	g := newGate(t, gatePct)

	withOpts := seg{
		srcIP: gateSrcIP, dstIP: gateDstIP,
		sport: 40110, dport: 8080, flags: tcpACK,
		tcpOptBytes: 20, // doff = 40, not the 20-byte minimum
		payload:     httpHead("GET", goldenDropLow, 0),
	}.build(t)

	if !g.run(withOpts) {
		t.Error("with TCP options present the gate must still find the payload")
	}
}

// Malformed data offsets: the guards that keep poff/plen arithmetic sane.
func TestGateRejectsMalformedDataOffset(t *testing.T) {
	g := newGate(t, gatePct)

	t.Run("doff below the minimum", func(t *testing.T) {
		s := seg{
			srcIP: gateSrcIP, dstIP: gateDstIP,
			sport: 40120, dport: 8080, flags: tcpACK,
			rawDoff: 3, // 12 bytes, under the 20-byte TCP header
			payload: httpHead("GET", goldenKeepLow, 0),
		}.build(t)
		// Rejected before the gate, so it is not counted as a gate drop --
		// the assertion that matters is that it does not crash or admit.
		g.run(s)
		if g.admitted(40120, 8080) {
			t.Error("a malformed segment must not admit a connection")
		}
	})

	t.Run("doff past the end of a short packet", func(t *testing.T) {
		s := seg{
			srcIP: gateSrcIP, dstIP: gateDstIP,
			sport: 40121, dport: 8080, flags: tcpACK,
			rawDoff: 15, // 60 bytes of header claimed, none present
		}.build(t)
		g.run(s)
		if g.admitted(40121, 8080) {
			t.Error("a segment whose payload offset runs past its end must not admit")
		}
	})
}
