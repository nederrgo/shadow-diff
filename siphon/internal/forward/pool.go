package forward

import (
	"context"
	"net"
	"sync"
	"time"
)

type Pool struct {
	dest        string
	sem         chan struct{}
	dialTimeout time.Duration
}

type PoolManager struct {
	mu          sync.RWMutex
	pools       map[string]*Pool
	maxActive   int
	dialTimeout time.Duration
}

func NewPoolManager(maxActive int, dialTimeout time.Duration) *PoolManager {
	if maxActive <= 0 {
		maxActive = 512
	}
	if dialTimeout <= 0 {
		dialTimeout = 2 * time.Second
	}
	return &PoolManager{
		pools:       make(map[string]*Pool),
		maxActive:   maxActive,
		dialTimeout: dialTimeout,
	}
}

func (pm *PoolManager) GetPool(dest string) *Pool {
	pm.mu.RLock()
	p, ok := pm.pools[dest]
	pm.mu.RUnlock()
	if ok {
		return p
	}

	pm.mu.Lock()
	defer pm.mu.Unlock()
	p, ok = pm.pools[dest]
	if ok {
		return p
	}

	p = &Pool{
		dest:        dest,
		sem:         make(chan struct{}, pm.maxActive),
		dialTimeout: pm.dialTimeout,
	}
	pm.pools[dest] = p
	return p
}

func (p *Pool) Dial(ctx context.Context) (net.Conn, error) {
	select {
	case p.sem <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	dialer := net.Dialer{Timeout: p.dialTimeout}
	conn, err := dialer.DialContext(ctx, "tcp", p.dest)
	if err != nil {
		<-p.sem // release slot
		return nil, err
	}

	return &pooledConn{
		Conn: conn,
		pool: p,
	}, nil
}

type pooledConn struct {
	net.Conn
	pool *Pool
	once sync.Once
}

func (c *pooledConn) Close() error {
	var err error
	c.once.Do(func() {
		err = c.Conn.Close()
		<-c.pool.sem // release slot
	})
	return err
}
