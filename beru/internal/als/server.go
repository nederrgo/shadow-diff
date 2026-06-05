package als

import (
	"io"
	"log/slog"
	"strings"

	accesslogv3 "github.com/envoyproxy/go-control-plane/envoy/data/accesslog/v3"
	alsv3 "github.com/envoyproxy/go-control-plane/envoy/service/accesslog/v3"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const mongoEgressLogPrefix = "mongo_egress_"

// Server implements Envoy gRPC Access Log Service for MongoDB egress.
type Server struct {
	alsv3.UnimplementedAccessLogServiceServer
	Log   *slog.Logger
	Store *Store
}

// StreamAccessLogs receives access log streams from Envoy sidecars.
func (s *Server) StreamAccessLogs(stream alsv3.AccessLogService_StreamAccessLogsServer) error {
	var streamRole string
	for {
		msg, err := stream.Recv()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return status.Errorf(codes.Unknown, "recv access log: %v", err)
		}
		if id := msg.GetIdentifier(); id != nil {
			if role := roleFromLogName(id.GetLogName()); role != "" {
				streamRole = role
			}
		}
		s.handleMessage(streamRole, msg)
	}
}

func roleFromLogName(logName string) string {
	if !strings.HasPrefix(logName, mongoEgressLogPrefix) {
		return ""
	}
	return strings.TrimPrefix(logName, mongoEgressLogPrefix)
}

func (s *Server) handleMessage(streamRole string, msg *alsv3.StreamAccessLogsMessage) {
	if msg == nil || s.Store == nil {
		return
	}
	if tcp := msg.GetTcpLogs(); tcp != nil {
		for _, entry := range tcp.GetLogEntry() {
			e := entry
			go s.Store.Handle(streamRole, "", e)
		}
	}
}

// HandleEntryForTest exposes entry handling for unit tests.
func (s *Server) HandleEntryForTest(streamRole, traceID string, entry *accesslogv3.TCPAccessLogEntry) {
	s.Store.Handle(streamRole, traceID, entry)
}
