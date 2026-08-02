// Package grpc serves the Monarch status stream consumed by Tusk and the live
// topology UI. The Hub fans reconcile-time status changes out to open streams;
// Server exposes them over gRPC as a controller-runtime Runnable.
package grpc

import (
	"sync"

	enginev1alpha1 "github.com/shadow-diff/monarch/api/v1alpha1"
	"github.com/shadow-diff/monarchpb"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// subscriberBuffer is how many updates a stream may fall behind before its
// oldest pending update is dropped. Status changes are whole-state snapshots,
// not deltas, so a dropped one is superseded by the next.
const subscriberBuffer = 16

type subscriber struct {
	ch        chan *monarchpb.ShadowTestStatusUpdate
	testName  string
	namespace string
}

// wants reports whether this subscriber's filter matches an update. An empty
// filter field matches anything, so a zero StatusRequest watches every test.
func (s *subscriber) wants(u *monarchpb.ShadowTestStatusUpdate) bool {
	if s.testName != "" && s.testName != u.GetTestName() {
		return false
	}
	if s.namespace != "" && s.namespace != u.GetNamespace() {
		return false
	}
	return true
}

// Hub broadcasts ShadowTest status changes to open gRPC streams. Client is the
// manager's cache-backed reader, used to snapshot current state when a stream opens.
type Hub struct {
	Client client.Client

	mu   sync.RWMutex
	subs map[int64]*subscriber
	next int64
}

func NewHub(c client.Client) *Hub {
	return &Hub{Client: c, subs: map[int64]*subscriber{}}
}

// Publish converts a ShadowTest and delivers it to every matching subscriber.
// It is called from the reconcile path, so it must never block.
//
// ponytail: a subscriber that is not draining loses updates once its 16-deep
// buffer fills — the send is non-blocking and the update is dropped on the floor.
// That is correct for whole-state snapshots (the next one supersedes it) and it
// keeps a wedged stream from stalling Reconcile. Ceiling: a consumer that is
// permanently slow sees a stale graph rather than a lagging one. Upgrade path:
// replace the channel with a per-test latest-value slot the reader coalesces on.
func (h *Hub) Publish(st *enginev1alpha1.ShadowTest) {
	if h == nil || st == nil {
		return
	}
	u := ToStatusUpdate(st)

	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, s := range h.subs {
		if !s.wants(u) {
			continue
		}
		select {
		case s.ch <- u:
		default:
		}
	}
}

// Subscribe registers a stream. An empty testName or namespace matches all.
// The returned func unregisters and closes the channel; it is safe to call once.
func (h *Hub) Subscribe(testName, namespace string) (<-chan *monarchpb.ShadowTestStatusUpdate, func()) {
	s := &subscriber{
		ch:        make(chan *monarchpb.ShadowTestStatusUpdate, subscriberBuffer),
		testName:  testName,
		namespace: namespace,
	}

	h.mu.Lock()
	id := h.next
	h.next++
	h.subs[id] = s
	h.mu.Unlock()

	return s.ch, func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		if _, ok := h.subs[id]; !ok {
			return
		}
		delete(h.subs, id)
		close(s.ch)
	}
}

// subscriberCount reports the number of live subscribers. Test hook.
func (h *Hub) subscriberCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.subs)
}
