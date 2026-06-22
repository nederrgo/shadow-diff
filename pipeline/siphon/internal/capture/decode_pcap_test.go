package capture

import (
	"encoding/binary"
	"testing"
	"time"
)

func TestDecodePcapAny_stripsMatchingHeader(t *testing.T) {
	frame := []byte{0x45, 0x00, 0x00, 0x28}
	hdr := make([]byte, 16)
	binary.LittleEndian.PutUint32(hdr[8:12], uint32(len(frame)))
	data := append(hdr, frame...)

	got, ci, ok := decodePcapAny(data)
	if !ok {
		t.Fatal("expected ok")
	}
	if string(got) != string(frame) {
		t.Fatalf("frame = %v want %v", got, frame)
	}
	if ci.Timestamp.IsZero() {
		t.Fatal("expected timestamp from header")
	}
}

func TestDecodePcapAny_mismatchedInclLenPassthrough(t *testing.T) {
	raw := []byte{0x45, 0x00, 0x00, 0x28, 0x01, 0x02, 0x03, 0x04}
	before := time.Now()
	got, ci, ok := decodePcapAny(raw)
	if !ok {
		t.Fatal("expected ok")
	}
	if string(got) != string(raw) {
		t.Fatalf("got %v want raw passthrough", got)
	}
	if ci.Timestamp.Before(before) {
		t.Fatal("expected time.Now fallback")
	}
}

func TestDecodePcapAny_shortPassthrough(t *testing.T) {
	raw := []byte{0x45, 0x00}
	got, ci, ok := decodePcapAny(raw)
	if !ok || len(got) != 2 {
		t.Fatalf("got %v ok=%v", got, ok)
	}
	if ci.Timestamp.IsZero() {
		t.Fatal("expected fallback timestamp")
	}
}

func TestDecodePcapAny_empty(t *testing.T) {
	_, _, ok := decodePcapAny(nil)
	if ok {
		t.Fatal("empty input should not be ok")
	}
	_, ci, ok := decodePcapAny([]byte{})
	if ok || !ci.Timestamp.IsZero() {
		t.Fatal("empty slice should not be ok")
	}
}
