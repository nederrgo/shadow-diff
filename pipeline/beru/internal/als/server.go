package als

import (
	"errors"
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
	log := s.Log
	if log == nil {
		log = slog.Default()
	}

	var streamRole string
	for {
		msg, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			if err := stream.SendAndClose(&alsv3.StreamAccessLogsResponse{}); err != nil {
				log.Error("ALS stream SendAndClose failed", "err", err)
				return status.Errorf(codes.Internal, "close access log stream: %v", err)
			}
			return nil
		}
		if err != nil {
			log.Error("ALS stream Recv failed", "err", err)
			return status.Errorf(codes.Unknown, "recv access log: %v", err)
		}
		if id := msg.GetIdentifier(); id != nil {
			if role := roleFromLogName(id.GetLogName()); role != "" {
				streamRole = role
			}
			log.Info("ALS stream identifier", "logName", id.GetLogName(), "role", streamRole)
		}
		s.handleMessage(log, streamRole, msg)
	}
}

func roleFromLogName(logName string) string {
	if !strings.HasPrefix(logName, mongoEgressLogPrefix) {
		return ""
	}
	return strings.TrimPrefix(logName, mongoEgressLogPrefix)
}

func (s *Server) handleMessage(log *slog.Logger, streamRole string, msg *alsv3.StreamAccessLogsMessage) {
	if msg == nil || s.Store == nil {
		return
	}
	switch entries := msg.GetLogEntries().(type) {
	case *alsv3.StreamAccessLogsMessage_TcpLogs:
		tcp := entries.TcpLogs
		if tcp == nil {
			return
		}
		n := len(tcp.GetLogEntry())
		if n > 0 {
			log.Info("ALS tcp batch", "role", streamRole, "entries", n)
		}
		for _, entry := range tcp.GetLogEntry() {
			s.Store.Handle(streamRole, "", entry)
		}
	case *alsv3.StreamAccessLogsMessage_HttpLogs:
		// HTTP ALS is not used for mongo egress; ignore without resetting the stream.
	default:
		return
	}
}

// HandleEntryForTest exposes entry handling for unit tests.
func (s *Server) HandleEntryForTest(streamRole, traceID string, entry *accesslogv3.TCPAccessLogEntry) {
	s.Store.Handle(streamRole, traceID, entry)
}
