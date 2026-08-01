package grpc

import (
	"context"
	"net"
	"testing"
	"time"

	enginev1alpha1 "github.com/shadow-diff/monarch/api/v1alpha1"
	"github.com/shadow-diff/monarchpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := enginev1alpha1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	return s
}

// startTestServer serves the status service over an in-memory listener.
func startTestServer(t *testing.T, c client.Client) (*Hub, monarchpb.MonarchStatusServiceClient) {
	t.Helper()
	hub := NewHub(c)

	lis := bufconn.Listen(1024 * 1024)
	srv := grpc.NewServer()
	monarchpb.RegisterMonarchStatusServiceServer(srv, &statusService{hub: hub})
	go func() { _ = srv.Serve(lis) }()

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = conn.Close()
		srv.Stop()
	})
	return hub, monarchpb.NewMonarchStatusServiceClient(conn)
}

// TestWatch_SendsSnapshotBeforeLiveUpdates is the behaviour the whole design
// rests on: a converged ShadowTest never reconciles again, so a UI attaching
// later must still receive its current state.
func TestWatch_SendsSnapshotBeforeLiveUpdates(t *testing.T) {
	existing := shadowTest("default", "already-ready")
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(existing).Build()

	hub, client := startTestServer(t, c)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	stream, err := client.WatchShadowTestStatus(ctx, &monarchpb.StatusRequest{})
	if err != nil {
		t.Fatal(err)
	}

	first, err := stream.Recv()
	if err != nil {
		t.Fatalf("expected snapshot, got err: %v", err)
	}
	if first.GetTestName() != "already-ready" {
		t.Fatalf("snapshot = %q, want already-ready", first.GetTestName())
	}

	// Now a live change must arrive on the same stream.
	hub.Publish(shadowTest("default", "brand-new"))
	second, err := stream.Recv()
	if err != nil {
		t.Fatalf("expected live update, got err: %v", err)
	}
	if second.GetTestName() != "brand-new" {
		t.Fatalf("live update = %q, want brand-new", second.GetTestName())
	}
}

func TestWatch_FiltersSnapshotByRequest(t *testing.T) {
	c := fake.NewClientBuilder().
		WithScheme(testScheme(t)).
		WithObjects(shadowTest("default", "alpha"), shadowTest("default", "beta"), shadowTest("other", "alpha")).
		Build()

	_, client := startTestServer(t, c)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	stream, err := client.WatchShadowTestStatus(ctx, &monarchpb.StatusRequest{
		TestName: "alpha", Namespace: "default",
	})
	if err != nil {
		t.Fatal(err)
	}

	got, err := stream.Recv()
	if err != nil {
		t.Fatal(err)
	}
	if got.GetTestName() != "alpha" || got.GetNamespace() != "default" {
		t.Fatalf("filter leaked: got %s/%s, want default/alpha", got.GetNamespace(), got.GetTestName())
	}
}

// A cancelled client must release its subscription, otherwise the hub leaks a
// channel per dropped browser.
func TestWatch_CancelUnsubscribes(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).Build()
	hub, client := startTestServer(t, c)

	ctx, cancel := context.WithCancel(context.Background())
	stream, err := client.WatchShadowTestStatus(ctx, &monarchpb.StatusRequest{})
	if err != nil {
		t.Fatal(err)
	}
	// WatchShadowTestStatus returns client-side before the server handler has
	// subscribed. With no ShadowTests to snapshot there is nothing to Recv that
	// would prove otherwise, so wait for the subscription to actually land —
	// publishing earlier would drop the update and block Recv forever.
	waitForSubscribers(t, hub, 1)

	hub.Publish(shadowTest("default", "alpha"))
	if _, err := stream.Recv(); err != nil {
		t.Fatal(err)
	}

	cancel()
	waitForSubscribers(t, hub, 0)
}

func waitForSubscribers(t *testing.T, h *Hub, want int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if h.subscriberCount() == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("subscriber count never reached %d (got %d)", want, h.subscriberCount())
}
