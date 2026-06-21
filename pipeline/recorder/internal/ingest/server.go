package ingest

import (
	"context"
	"log"
	"net"
	"sync"
)

// Server accepts TCP connections from Siphon and deframes egress relay traffic.
type Server struct {
	listenAddr string
	store      *SessionStore
	ln         net.Listener
	wg         sync.WaitGroup
}

// NewServer creates a TCP ingest server.
func NewServer(listenAddr string, store *SessionStore) *Server {
	return &Server{listenAddr: listenAddr, store: store}
}

// Listen starts accepting connections. Blocks until ctx is cancelled.
func (s *Server) Listen(ctx context.Context) error {
	ln, err := net.Listen("tcp", s.listenAddr)
	if err != nil {
		return err
	}
	s.ln = ln

	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()

	log.Printf("recorder: listening on %s", s.listenAddr)
	for {
		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				s.wg.Wait()
				return nil
			default:
			}
			if ne, ok := err.(net.Error); ok && ne.Temporary() {
				log.Printf("recorder: accept: %v", err)
				continue
			}
			return err
		}
		connID := s.store.RegisterConn()
		log.Printf("recorder debug: siphon conn=%d from %s", connID, conn.RemoteAddr())
		s.wg.Add(1)
		go func(c net.Conn, id uint64) {
			defer s.wg.Done()
			HandleConn(c, s.store, id)
		}(conn, connID)
	}
}
