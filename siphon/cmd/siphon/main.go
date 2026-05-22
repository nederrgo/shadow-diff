package main

import (
	"log"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/shadow-diff/siphon/internal/api"
	"github.com/shadow-diff/siphon/internal/assembly"
	"github.com/shadow-diff/siphon/internal/capture"
	"github.com/shadow-diff/siphon/internal/config"
	"github.com/shadow-diff/siphon/internal/forward"
	"github.com/shadow-diff/siphon/internal/session"
)

func main() {
	log.Println("Initializing Siphon Agent...")

	// 1. Load Environment Variables
	apiAddr := os.Getenv("SIPHON_API_ADDR")
	if apiAddr == "" {
		apiAddr = ":8080"
	}

	ifaceEnv := os.Getenv("SIPHON_INTERFACE")
	if ifaceEnv == "" {
		ifaceEnv = "any"
	}

	ttl := 5 * time.Minute
	if ttlStr := os.Getenv("SIPHON_SESSION_TTL"); ttlStr != "" {
		if parsed, err := time.ParseDuration(ttlStr); err == nil {
			ttl = parsed
		} else {
			log.Printf("Invalid SIPHON_SESSION_TTL %q, using default of 5m: %v", ttlStr, err)
		}
	}

	maxSessions := 100000
	if maxSessionsStr := os.Getenv("SIPHON_SESSION_MAX"); maxSessionsStr != "" {
		if parsed, err := strconv.Atoi(maxSessionsStr); err == nil && parsed > 0 {
			maxSessions = parsed
		} else {
			log.Printf("Invalid SIPHON_SESSION_MAX %q, using default of 100k: %v", maxSessionsStr, err)
		}
	}

	maxConns := 512
	if maxConnsStr := os.Getenv("SIPHON_IGRIS_MAX_CONNS"); maxConnsStr != "" {
		if parsed, err := strconv.Atoi(maxConnsStr); err == nil && parsed > 0 {
			maxConns = parsed
		} else {
			log.Printf("Invalid SIPHON_IGRIS_MAX_CONNS %q, using default of 512: %v", maxConnsStr, err)
		}
	}

	// 2. Instantiate Components
	cfgMgr := config.NewManager()
	sessionMap := session.NewSessionMap(ttl, maxSessions)

	var requestsForwarded uint64
	poolMgr := forward.NewPoolManager(maxConns, 2*time.Second)
	factory := assembly.NewStreamFactory(cfgMgr, poolMgr, &requestsForwarded)
	capMgr := capture.NewCaptureManager(cfgMgr, sessionMap, factory)

	// 3. Start Control API Server
	server := api.NewServer(apiAddr, cfgMgr, sessionMap, capMgr, &requestsForwarded, ifaceEnv)

	go func() {
		if err := server.Start(); err != nil {
			log.Fatalf("Control API Server failed: %v", err)
		}
	}()

	// 4. Graceful Shutdown handling
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	sig := <-sigChan
	log.Printf("Received signal %v. Shutting down Siphon Agent...", sig)

	capMgr.Stop()
	log.Println("Siphon Agent exited successfully.")
}
