package server

import (
	"context"
	"log/slog"
	"time"

	"github.com/shadow-diff/monarchpb"
	"github.com/shadow-diff/tusk/pkg/topology"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

const (
	reconnectMin = time.Second
	reconnectMax = 30 * time.Second
)

// MonarchClient consumes Monarch's status stream and feeds the WebSocket Hub.
//
// One stream serves the whole process regardless of how many browsers are
// connected: it watches every ShadowTest (empty StatusRequest) and the Hub does
// the per-client filtering. Opening a stream per browser would multiply load on
// the operator for no benefit.
type MonarchClient struct {
	Addr string
	Hub  *Hub
	Log  *slog.Logger
}

// Run maintains the stream until ctx is cancelled, reconnecting with capped
// backoff so a Monarch rollout degrades the feed rather than ending it.
func (m *MonarchClient) Run(ctx context.Context) error {
	conn, err := grpc.NewClient(m.Addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()

	client := monarchpb.NewMonarchStatusServiceClient(conn)
	backoff := reconnectMin

	for {
		if err := m.consume(ctx, client); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			m.Log.Warn("Monarch status stream ended; retrying", "err", err, "in", backoff)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
		if backoff *= 2; backoff > reconnectMax {
			backoff = reconnectMax
		}
	}
}

// consume runs one stream to completion. A clean return means the server closed
// the stream; the caller reconnects either way.
func (m *MonarchClient) consume(ctx context.Context, client monarchpb.MonarchStatusServiceClient) error {
	stream, err := client.WatchShadowTestStatus(ctx, &monarchpb.StatusRequest{})
	if err != nil {
		return err
	}
	m.Log.Info("Watching Monarch status stream", "addr", m.Addr)

	for {
		update, err := stream.Recv()
		if err != nil {
			return err
		}
		m.Hub.Broadcast(topology.BuildTopologyGraph(update))
	}
}
