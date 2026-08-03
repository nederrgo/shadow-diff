package controller

import (
	"context"
	"net/http"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	enginev1alpha1 "github.com/shadow-diff/monarch/api/v1alpha1"
)

func TestMaybeTriggerReplay_ConflictSetsStarted(t *testing.T) {
	scheme := deleteLifecycleScheme(t)
	st := &enginev1alpha1.ShadowTest{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "replay-409",
			Namespace: "default",
			UID:       "eeeeeeee-eeee-eeee-eeee-eeeeeeeeeeee",
		},
		Spec: enginev1alpha1.ShadowTestSpec{
			Mode:      modeReplay,
			SessionID: "session-1",
			NewImage:  "busybox:1.36",
			Storage: &enginev1alpha1.StorageConfig{
				Type:       "s3",
				BucketName: "b",
			},
		},
		Status: enginev1alpha1.ShadowTestStatus{
			CurrentSessionID: "session-1",
		},
	}
	shadowNS := "shadow-default-replay-409"

	objs := []client.Object{st.DeepCopy()}
	for _, name := range replayWorkloadNames(st) {
		objs = append(objs, rollReadyDeploy(shadowNS, name, string(st.UID)))
	}

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objs...).
		WithStatusSubresource(&enginev1alpha1.ShadowTest{}).
		Build()
	rec := &ShadowTestReconciler{
		Client: c,
		Scheme: scheme,
		ReplayStarter: func(ctx context.Context, url string) (int, error) {
			return http.StatusConflict, nil
		},
	}
	// maybeTriggerReplay patches the passed-in object; refresh from client first.
	var live enginev1alpha1.ShadowTest
	if err := c.Get(context.Background(), types.NamespacedName{Name: st.Name, Namespace: st.Namespace}, &live); err != nil {
		t.Fatalf("get: %v", err)
	}
	requeue, err := rec.maybeTriggerReplay(context.Background(), &live, shadowNS)
	if err != nil {
		t.Fatalf("maybeTriggerReplay: %v", err)
	}
	if requeue {
		t.Fatal("expected no requeue after 409")
	}
	var after enginev1alpha1.ShadowTest
	if err := c.Get(context.Background(), types.NamespacedName{Name: st.Name, Namespace: st.Namespace}, &after); err != nil {
		t.Fatalf("get after: %v", err)
	}
	if after.Status.ReplayState != replayStateStarted {
		t.Fatalf("ReplayState = %q, want %s", after.Status.ReplayState, replayStateStarted)
	}
}

func rollReadyDeploy(ns, name, uid string) *appsv1.Deployment {
	replicas := int32(1)
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
			Labels: map[string]string{
				labelManagedBy:     valueManagedBy,
				labelShadowTestUID: uid,
			},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": name}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": name}},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "c", Image: "busybox"}}},
			},
		},
		Status: appsv1.DeploymentStatus{
			Replicas:          1,
			ReadyReplicas:     1,
			UpdatedReplicas:   1,
			AvailableReplicas: 1,
		},
	}
}

func TestDeploymentsRollReady_RequiresAllReplicas(t *testing.T) {
	scheme := deleteLifecycleScheme(t)
	ns := "shadow-roll-ready"
	replicas := int32(2)
	partial := rollReadyDeploy(ns, "shop", "uid")
	partial.Spec.Replicas = &replicas
	partial.Status.Replicas = 2
	partial.Status.UpdatedReplicas = 2
	partial.Status.ReadyReplicas = 1 // one of two Ready — not enough

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(partial).Build()
	rec := &ShadowTestReconciler{Client: c, Scheme: scheme}
	ready, err := rec.deploymentsRollReady(context.Background(), ns, []string{"shop"})
	if err != nil {
		t.Fatalf("deploymentsRollReady: %v", err)
	}
	if ready {
		t.Fatal("ReadyReplicas=1 of 2 must not be roll-ready")
	}

	partial.Status.ReadyReplicas = 2
	if err := c.Status().Update(context.Background(), partial); err != nil {
		t.Fatalf("status update: %v", err)
	}
	ready, err = rec.deploymentsRollReady(context.Background(), ns, []string{"shop"})
	if err != nil || !ready {
		t.Fatalf("ReadyReplicas=2 of 2: ready=%v err=%v", ready, err)
	}
}
