package session

import (
	"hash/fnv"
	"strconv"
	"sync"
	"time"
)

type FourTuple struct {
	IP1, IP2     string
	Port1, Port2 uint16
}

func MakeFourTuple(srcIP string, srcPort uint16, dstIP string, dstPort uint16) FourTuple {
	if srcIP < dstIP || (srcIP == dstIP && srcPort <= dstPort) {
		return FourTuple{IP1: srcIP, IP2: dstIP, Port1: srcPort, Port2: dstPort}
	}
	return FourTuple{IP1: dstIP, IP2: srcIP, Port1: dstPort, Port2: srcPort}
}

func (t FourTuple) String() string {
	return t.IP1 + ":" + strconv.Itoa(int(t.Port1)) + "-" + t.IP2 + ":" + strconv.Itoa(int(t.Port2))
}

type SessionState struct {
	Sampled  bool
	LastSeen time.Time
}

type SessionMap struct {
	mu         sync.RWMutex
	sessions   map[FourTuple]*SessionState
	ttl        time.Duration
	maxEntries int
}

func NewSessionMap(ttl time.Duration, maxEntries int) *SessionMap {
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	if maxEntries <= 0 {
		maxEntries = 100000
	}
	sm := &SessionMap{
		sessions:   make(map[FourTuple]*SessionState),
		ttl:        ttl,
		maxEntries: maxEntries,
	}
	go sm.evictionLoop()
	return sm
}

func (sm *SessionMap) GetOrDecide(srcIP string, srcPort uint16, dstIP string, dstPort uint16, sampleRate int) bool {
	key := MakeFourTuple(srcIP, srcPort, dstIP, dstPort)

	sm.mu.RLock()
	state, exists := sm.sessions[key]
	if exists {
		state.LastSeen = time.Now()
		sampled := state.Sampled
		sm.mu.RUnlock()
		return sampled
	}
	sm.mu.RUnlock()

	sm.mu.Lock()
	defer sm.mu.Unlock()

	// Double check
	state, exists = sm.sessions[key]
	if exists {
		state.LastSeen = time.Now()
		return state.Sampled
	}

	// Evict if we exceed maxEntries to prevent out of memory
	if len(sm.sessions) >= sm.maxEntries {
		now := time.Now()
		deleted := 0
		for k, s := range sm.sessions {
			if now.Sub(s.LastSeen) > sm.ttl {
				delete(sm.sessions, k)
				deleted++
			}
		}
		// If we still exceed, delete oldest or arbitrary entries to make space
		if len(sm.sessions) >= sm.maxEntries {
			for k := range sm.sessions {
				delete(sm.sessions, k)
				deleted++
				if deleted > sm.maxEntries/10 {
					break
				}
			}
		}
	}

	// Compute FNV-1a hash
	h := fnv.New64a()
	h.Write([]byte(key.String()))
	hashVal := h.Sum64()

	sampled := (hashVal % 100) < uint64(sampleRate)
	sm.sessions[key] = &SessionState{
		Sampled:  sampled,
		LastSeen: time.Now(),
	}
	return sampled
}

func (sm *SessionMap) ActiveCount() int {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return len(sm.sessions)
}

func (sm *SessionMap) evictionLoop() {
	ticker := time.NewTicker(30 * time.Second)
	for range ticker.C {
		sm.mu.Lock()
		now := time.Now()
		for k, s := range sm.sessions {
			if now.Sub(s.LastSeen) > sm.ttl {
				delete(sm.sessions, k)
			}
		}
		sm.mu.Unlock()
	}
}
