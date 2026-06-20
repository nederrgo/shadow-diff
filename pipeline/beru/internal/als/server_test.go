package als

import (
	"context"
	"net"
	"testing"
	"time"

	accesslogv3 "github.com/envoyproxy/go-control-plane/envoy/data/accesslog/v3"
	corev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	alsv3 "github.com/envoyproxy/go-control-plane/envoy/service/accesslog/v3"
	"github.com/shadow-diff/beru/internal/ingest"
	"github.com/shadow-diff/beru/internal/roles"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/structpb"
)

const bufSize = 1 << 20

func startTestALSServer(t *testing.T) (*grpc.ClientConn, *Store) {
	t.Helper()

	cfg := ingest.Config{TraceTTL: 30 * time.Second, MaxPendingTraces: 100, SweepInterval: time.Hour}
	store := NewStore(nil, cfg)
	alsServer := &Server{Store: store}

	lis := bufconn.Listen(bufSize)
	grpcServer := grpc.NewServer()
	alsv3.RegisterAccessLogServiceServer(grpcServer, alsServer)

	go func() {
		if err := grpcServer.Serve(lis); err != nil {
			t.Logf("grpc serve: %v", err)
		}
	}()
	t.Cleanup(grpcServer.Stop)

	dialer := func(context.Context, string) (net.Conn, error) {
		return lis.Dial()
	}
	conn, err := grpc.DialContext(context.Background(), "bufnet",
		grpc.WithContextDialer(dialer),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	return conn, store
}

func TestStreamAccessLogs_tcpBatch(t *testing.T) {
	conn, store := startTestALSServer(t)
	client := alsv3.NewAccessLogServiceClient(conn)

	stream, err := client.StreamAccessLogs(context.Background())
	if err != nil {
		t.Fatalf("StreamAccessLogs: %v", err)
	}

	if err := stream.Send(&alsv3.StreamAccessLogsMessage{
		Identifier: &alsv3.StreamAccessLogsMessage_Identifier{
			LogName: "mongo_egress_control-a",
		},
	}); err != nil {
		t.Fatalf("send identifier: %v", err)
	}

	st, _ := structpb.NewStruct(map[string]any{
		"test.items": []any{"insert"},
	})

	for _, role := range roles.All {
		e := &accesslogv3.TCPAccessLogEntry{
			CommonProperties: &accesslogv3.AccessLogCommon{
				StreamId:   "conn-1",
				CustomTags: map[string]string{tagShadowRole: role},
				Metadata: &corev3.Metadata{
					FilterMetadata: map[string]*structpb.Struct{
						mongoFilterMetadata: st,
					},
				},
			},
		}
		if err := stream.Send(&alsv3.StreamAccessLogsMessage{
			LogEntries: &alsv3.StreamAccessLogsMessage_TcpLogs{
				TcpLogs: &alsv3.StreamAccessLogsMessage_TCPAccessLogEntries{
					LogEntry: []*accesslogv3.TCPAccessLogEntry{e},
				},
			},
		}); err != nil {
			t.Fatalf("send tcp logs for %s: %v", role, err)
		}
	}

	store.NotifyIngressComplete("trace-grpc")
	if _, err := stream.CloseAndRecv(); err != nil {
		t.Fatalf("CloseAndRecv: %v", err)
	}
}
