package consumer

import (
	"crypto/sha256"
	"fmt"
	"testing"
	"time"
)

func TestPublishDedup_blocksDuplicateWithinWindow(t *testing.T) {
	d := newPublishDedup()
	payload := []byte(`{"order_id":"1"}`)
	if !d.shouldForward("trace1", "span1", payload) {
		t.Fatal("first message should forward")
	}
	if d.shouldForward("trace1", "span1", payload) {
		t.Fatal("duplicate within window should be blocked")
	}
}

func TestPublishDedup_forwardsAfterWindow(t *testing.T) {
	d := newPublishDedup()
	payload := []byte(`{"order_id":"1"}`)
	sum := sha256.Sum256(payload)
	key := fmt.Sprintf("%s:%s:%x", "trace1", "span1", sum[:8])
	d.recentPublishes.Store(key, time.Now().Add(-dedupWindow-time.Millisecond))
	if !d.shouldForward("trace1", "span1", payload) {
		t.Fatal("stale entry should allow forward")
	}
}

func TestPublishDedup_differentPayloadForwards(t *testing.T) {
	d := newPublishDedup()
	a := []byte(`{"order_id":"1"}`)
	b := []byte(`{"order_id":"1","extra":"duplicate"}`)
	if !d.shouldForward("trace1", "span1", a) {
		t.Fatal("first payload should forward")
	}
	if !d.shouldForward("trace1", "span1", b) {
		t.Fatal("different payload should forward even within window")
	}
}

func TestPublishDedup_emptySpanIDNeverBlocks(t *testing.T) {
	d := newPublishDedup()
	payload := []byte(`{"order_id":"1"}`)
	if !d.shouldForward("trace1", "", payload) {
		t.Fatal("first message should forward")
	}
	if !d.shouldForward("trace1", "", payload) {
		t.Fatal("empty span_id should never block")
	}
}
