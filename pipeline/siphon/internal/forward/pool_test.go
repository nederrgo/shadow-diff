package forward

import (
	"context"
	"net"
	"testing"
	"time"
)

func TestPoolConcurrencyAndDial(t *testing.T) {
	// Start a local test server
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Failed to start local test listener: %v", err)
	}
	defer ln.Close()

	dest := ln.Addr().String()

	// Create pool manager with limit of 2 concurrent connections
	pm := NewPoolManager(2, 50*time.Millisecond)
	pool := pm.GetPool(dest)

	ctx := context.Background()

	// 1. Dial first connection
	c1, err := pool.Dial(ctx)
	if err != nil {
		t.Fatalf("Dial 1 failed: %v", err)
	}

	// 2. Dial second connection
	c2, err := pool.Dial(ctx)
	if err != nil {
		t.Fatalf("Dial 2 failed: %v", err)
	}

	// 3. Dial third connection (should fail or timeout since max is 2)
	ctxTimeout, cancel := context.WithTimeout(ctx, 10*time.Millisecond)
	defer cancel()
	_, err = pool.Dial(ctxTimeout)
	if err == nil {
		t.Fatal("Expected Dial 3 to fail due to concurrency limit")
	}

	// 4. Close first connection and try again (should succeed)
	_ = c1.Close()

	c3, err := pool.Dial(ctx)
	if err != nil {
		t.Fatalf("Dial 3 should succeed after c1 closed, got: %v", err)
	}

	_ = c2.Close()
	_ = c3.Close()
}
