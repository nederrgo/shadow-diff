package egress

import (
	"bytes"
	"testing"
)

func TestHTTPLegComplete_contentLength(t *testing.T) {
	req := []byte("POST /post HTTP/1.1\r\nContent-Length: 4\r\n\r\n{\"a\"}")
	if !httpLegComplete(req) {
		t.Fatal("expected complete request")
	}
	short := []byte("POST /post HTTP/1.1\r\nContent-Length: 16\r\n\r\n{\"a\"}")
	if httpLegComplete(short) {
		t.Fatal("expected incomplete request")
	}
	res := []byte("HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nok")
	if !httpLegComplete(res) {
		t.Fatal("expected complete response")
	}
}

func TestHTTPLegComplete_noContentLength(t *testing.T) {
	get := []byte("GET / HTTP/1.1\r\nHost: x\r\n\r\n")
	if !httpLegComplete(get) {
		t.Fatal("GET without body should be complete after headers")
	}
}

func TestFinalizeTruncatedResponse_midHeaderPCA(t *testing.T) {
	// httpbin-style headers exceed a single ~190B PCA snap; no header terminator.
	trunc := []byte("HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: 542\r\nDate: Sun, 21 Jun 2026")
	fixed := finalizeTruncatedResponse(trunc)
	if !httpLegComplete(fixed) {
		t.Fatalf("expected synthesized complete response, got %q", fixed)
	}
}

func TestNormalizeTruncatedHTTP_shrinksContentLength(t *testing.T) {
	trunc := []byte("HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: 350\r\n\r\n{\"partial\":")
	fixed := normalizeTruncatedHTTP(trunc)
	if !httpLegComplete(fixed) {
		t.Fatalf("expected complete after normalize, got %q", fixed)
	}
	idx := bytes.Index(fixed, []byte("\r\n\r\n"))
	body := fixed[idx+4:]
	if parseContentLength(fixed[:idx]) != len(body) {
		t.Fatalf("Content-Length should match body: %q", fixed)
	}
}

func TestApplyRequestTruncationFallback_partialHeadersNoTerminator(t *testing.T) {
	// PCA snap may include CL + JSON body without a preceding \r\n\r\n.
	trunc := []byte("POST /v1/log HTTP/1.1\r\nHost: x\r\nContent-Type: application/json\r\nContent-Length: 58\r\n{\"status\": \"complete\", \"order_id\": \"e2e-")
	fixed, applied := applyRequestTruncationFallback(trunc)
	if !applied {
		t.Fatal("expected partial-header fallback")
	}
	if !httpLegComplete(fixed) {
		t.Fatalf("expected complete request, got %q", fixed)
	}
	idx := bytes.Index(fixed, []byte("\r\n\r\n"))
	if parseContentLength(fixed[:idx]) != len(fixed[idx+4:]) {
		t.Fatalf("Content-Length should match body: %q", fixed)
	}
}

func TestApplyRequestTruncationFallback_logsAndRewrites(t *testing.T) {
	trunc := []byte("POST /v1/log HTTP/1.1\r\nHost: user-service.prod.internal\r\nUser-Agent: python-requests/2.32.3\r\nAccept-Encoding: gzip, deflate\r\nAccept: */*\r\nConnection: keep-alive\r\nContent-Type: application/json\r\nContent-Length: 58\r\n\r\n{\"status\": \"complete\", \"order_id\": \"e2e-")
	trunc = trunc[:248]
	fixed, applied := applyRequestTruncationFallback(trunc)
	if !applied {
		t.Fatal("expected fallback to apply on 248B PCA snap")
	}
	if !httpLegComplete(fixed) {
		t.Fatalf("expected complete request after fallback, got %q", fixed)
	}
	idx := bytes.Index(fixed, []byte("\r\n\r\n"))
	if parseContentLength(fixed[:idx]) != len(fixed[idx+4:]) {
		t.Fatalf("Content-Length should match captured body: %q", fixed)
	}
}

func TestFinalize248RealPCA(t *testing.T) {
	// python-requests POST to user-service: ~274B total; PCA snap often stops at 248B.
	trunc := []byte("POST /v1/log HTTP/1.1\r\nHost: user-service.prod.internal\r\nUser-Agent: python-requests/2.32.3\r\nAccept-Encoding: gzip, deflate\r\nAccept: */*\r\nConnection: keep-alive\r\nContent-Type: application/json\r\nContent-Length: 58\r\n\r\n{\"status\": \"complete\", \"order_id\": \"e2e-")
	trunc = trunc[:248]
	fixed := finalizeTruncatedRequest(trunc)
	if !httpLegComplete(fixed) {
		t.Fatalf("expected complete request after 248B PCA snap, got %q", fixed)
	}
}

func TestFinalizeTruncatedRequest_shrinksContentLength(t *testing.T) {
	// python-requests POST: headers + partial JSON body (PCA ~248B snap).
	trunc := []byte("POST /v1/log HTTP/1.1\r\nHost: user-service.prod.internal\r\nContent-Type: application/json\r\nContent-Length: 58\r\n\r\n{\"status\": \"complete\", \"order_id\": \"e2e-")
	fixed := finalizeTruncatedRequest(trunc)
	if !httpLegComplete(fixed) {
		t.Fatalf("expected complete request after finalize, got %q", fixed)
	}
	idx := bytes.Index(fixed, []byte("\r\n\r\n"))
	if parseContentLength(fixed[:idx]) != len(fixed[idx+4:]) {
		t.Fatalf("Content-Length should match captured body: %q", fixed)
	}
}
