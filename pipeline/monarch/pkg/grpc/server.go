package grpc

import (
	"context"
	"net"

	enginev1alpha1 "github.com/shadow-diff/monarch/api/v1alpha1"
	"github.com/shadow-diff/monarchpb"
	"google.golang.org/grpc"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

// Server exposes the Hub over gRPC. It satisfies controller-runtime's
// manager.Runnable so the manager owns its lifecycle, and opts out of leader
// election so every replica can serve reads.
type Server struct {
	Addr string
	Hub  *Hub
}

// NeedLeaderElection reports false: streaming status is a read-only concern and
// should be available on all replicas, not just the leader.
func (s *Server) NeedLeaderElection() bool { return false }

// Start blocks serving gRPC until ctx is cancelled.
func (s *Server) Start(ctx context.Context) error {
	log := logf.FromContext(ctx).WithName("status-grpc")

	lis, err := net.Listen("tcp", s.Addr)
	if err != nil {
		return err
	}

	srv := grpc.NewServer()
	monarchpb.RegisterMonarchStatusServiceServer(srv, &statusService{hub: s.Hub})

	go func() {
		<-ctx.Done()
		log.Info("Shutting down status gRPC server")
		srv.GracefulStop()
	}()

	log.Info("Status gRPC server listening", "addr", s.Addr)
	if err := srv.Serve(lis); err != nil {
		return err
	}
	return nil
}

type statusService struct {
	monarchpb.UnimplementedMonarchStatusServiceServer
	hub *Hub
}

// WatchShadowTestStatus sends a snapshot of matching ShadowTests, then streams
// every subsequent change.
//
// The subscription is registered before the snapshot is read so a status change
// landing mid-snapshot is queued rather than lost. That can duplicate one update
// (snapshot plus the queued change), which is harmless: updates are whole-state,
// so applying the same one twice is a no-op for the consumer.
func (s *statusService) WatchShadowTestStatus(
	req *monarchpb.StatusRequest,
	stream monarchpb.MonarchStatusService_WatchShadowTestStatusServer,
) error {
	ctx := stream.Context()
	name, namespace := req.GetTestName(), req.GetNamespace()

	updates, unsubscribe := s.hub.Subscribe(name, namespace)
	defer unsubscribe()

	if err := s.sendSnapshot(ctx, stream, name, namespace); err != nil {
		return err
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case u, ok := <-updates:
			if !ok {
				return nil
			}
			if err := stream.Send(u); err != nil {
				return err
			}
		}
	}
}

// sendSnapshot emits one update per ShadowTest currently matching the filter, so
// a consumer attaching to an already-converged test sees its state immediately
// rather than waiting for a change that may never come.
func (s *statusService) sendSnapshot(
	ctx context.Context,
	stream monarchpb.MonarchStatusService_WatchShadowTestStatusServer,
	name, namespace string,
) error {
	if s.hub == nil || s.hub.Client == nil {
		return nil
	}

	var list enginev1alpha1.ShadowTestList
	if err := s.hub.Client.List(ctx, &list); err != nil {
		return err
	}
	for i := range list.Items {
		st := &list.Items[i]
		if name != "" && st.Name != name {
			continue
		}
		if namespace != "" && st.Namespace != namespace {
			continue
		}
		if err := stream.Send(ToStatusUpdate(st)); err != nil {
			return err
		}
	}
	return nil
}
