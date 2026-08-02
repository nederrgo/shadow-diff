package grpc

import (
	"testing"
	"time"

	enginev1alpha1 "github.com/shadow-diff/monarch/api/v1alpha1"
	"github.com/shadow-diff/monarchpb"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func shadowTest(namespace, name string) *enginev1alpha1.ShadowTest {
	return &enginev1alpha1.ShadowTest{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Status:     enginev1alpha1.ShadowTestStatus{Phase: enginev1alpha1.PhaseReady},
	}
}

func recvWithin(t *testing.T, ch <-chan *monarchpb.ShadowTestStatusUpdate, d time.Duration) *monarchpb.ShadowTestStatusUpdate {
	t.Helper()
	select {
	case u := <-ch:
		return u
	case <-time.After(d):
		t.Fatal("timed out waiting for update")
		return nil
	}
}

func TestHub_PublishReachesMatchingSubscriber(t *testing.T) {
	h := NewHub(nil)
	ch, unsubscribe := h.Subscribe("", "")
	defer unsubscribe()

	h.Publish(shadowTest("default", "alpha"))

	got := recvWithin(t, ch, time.Second)
	if got.GetTestName() != "alpha" || got.GetNamespace() != "default" {
		t.Fatalf("got %s/%s", got.GetNamespace(), got.GetTestName())
	}
	if got.GetPhase() != monarchpb.Phase_PHASE_READY {
		t.Fatalf("phase = %v", got.GetPhase())
	}
}

func TestHub_FilterExcludesOtherTests(t *testing.T) {
	h := NewHub(nil)
	ch, unsubscribe := h.Subscribe("alpha", "default")
	defer unsubscribe()

	h.Publish(shadowTest("default", "beta"))  // wrong name
	h.Publish(shadowTest("other", "alpha"))   // wrong namespace
	h.Publish(shadowTest("default", "alpha")) // match

	got := recvWithin(t, ch, time.Second)
	if got.GetTestName() != "alpha" || got.GetNamespace() != "default" {
		t.Fatalf("filter leaked: got %s/%s", got.GetNamespace(), got.GetTestName())
	}
	select {
	case extra := <-ch:
		t.Fatalf("unexpected extra update: %s/%s", extra.GetNamespace(), extra.GetTestName())
	default:
	}
}

// TestHub_SlowSubscriberDoesNotBlockPublish is the important one: Publish runs on
// the reconcile path, so a stream that stops draining must never stall it.
func TestHub_SlowSubscriberDoesNotBlockPublish(t *testing.T) {
	h := NewHub(nil)
	_, unsubscribe := h.Subscribe("", "")
	defer unsubscribe()

	done := make(chan struct{})
	go func() {
		defer close(done)
		// Far more than subscriberBuffer, with nothing reading.
		for i := 0; i < subscriberBuffer*10; i++ {
			h.Publish(shadowTest("default", "alpha"))
		}
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Publish blocked on a subscriber that is not draining")
	}
}

func TestHub_UnsubscribeIsIdempotentAndStopsDelivery(t *testing.T) {
	h := NewHub(nil)
	ch, unsubscribe := h.Subscribe("", "")

	unsubscribe()
	unsubscribe() // must not panic on a double close

	if n := h.subscriberCount(); n != 0 {
		t.Fatalf("subscriberCount = %d, want 0", n)
	}
	h.Publish(shadowTest("default", "alpha")) // must not send on a closed channel

	if _, open := <-ch; open {
		t.Fatal("channel should be closed after unsubscribe")
	}
}

func TestHub_PublishNilIsSafe(t *testing.T) {
	h := NewHub(nil)
	h.Publish(nil)
	var nilHub *Hub
	nilHub.Publish(shadowTest("default", "alpha"))
}
