// Package server wires Monarch's gRPC status stream to browser WebSockets.
package server

import (
	"sync"

	"github.com/shadow-diff/tusk/pkg/topology"
)

// clientBuffer is how many graphs a browser may fall behind before the oldest is
// dropped. Graphs are whole-state, so a dropped one is superseded by the next.
const clientBuffer = 8

type testKey struct{ namespace, name string }

type wsClient struct {
	ch     chan *topology.TopologyGraph
	filter testKey
}

func (c *wsClient) wants(k testKey) bool {
	if c.filter.name != "" && c.filter.name != k.name {
		return false
	}
	if c.filter.namespace != "" && c.filter.namespace != k.namespace {
		return false
	}
	return true
}

// Hub caches the latest graph per ShadowTest and fans updates out to browsers.
// The cache is what a newly-connected browser receives immediately, mirroring the
// snapshot Monarch sends when Tusk's own stream opens — without it a client
// attaching to a converged ShadowTest would see an empty canvas.
type Hub struct {
	mu      sync.RWMutex
	latest  map[testKey]*topology.TopologyGraph
	clients map[int64]*wsClient
	next    int64
}

func NewHub() *Hub {
	return &Hub{
		latest:  map[testKey]*topology.TopologyGraph{},
		clients: map[int64]*wsClient{},
	}
}

// Broadcast caches a graph and delivers it to every matching client.
// A Phase == "Deleted" tombstone evicts the cache entry but is still delivered
// so browsers can clear their canvas.
//
// ponytail: a browser that stops reading loses frames once its buffer fills;
// the send is non-blocking so one stalled socket cannot back up the gRPC reader.
// Ceiling: a permanently slow client shows a stale graph. Upgrade path: keep a
// per-client latest-value slot instead of a queue.
func (h *Hub) Broadcast(g *topology.TopologyGraph) {
	if g == nil {
		return
	}
	k := testKey{namespace: g.Namespace, name: g.TestName}

	h.mu.Lock()
	if g.Phase == "Deleted" {
		delete(h.latest, k)
	} else {
		h.latest[k] = g
	}
	clients := make([]*wsClient, 0, len(h.clients))
	for _, c := range h.clients {
		if c.wants(k) {
			clients = append(clients, c)
		}
	}
	h.mu.Unlock()

	for _, c := range clients {
		select {
		case c.ch <- g:
		default:
		}
	}
}

// Subscribe registers a browser. Empty name or namespace matches all tests.
// The returned slice is the cached state to send before streaming.
func (h *Hub) Subscribe(namespace, name string) (<-chan *topology.TopologyGraph, []*topology.TopologyGraph, func()) {
	c := &wsClient{
		ch:     make(chan *topology.TopologyGraph, clientBuffer),
		filter: testKey{namespace: namespace, name: name},
	}

	h.mu.Lock()
	id := h.next
	h.next++
	h.clients[id] = c
	snapshot := make([]*topology.TopologyGraph, 0, len(h.latest))
	for k, g := range h.latest {
		if c.wants(k) {
			snapshot = append(snapshot, g)
		}
	}
	h.mu.Unlock()

	return c.ch, snapshot, func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		if _, ok := h.clients[id]; !ok {
			return
		}
		delete(h.clients, id)
		close(c.ch)
	}
}
