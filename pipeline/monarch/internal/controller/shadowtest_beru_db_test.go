package controller

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	enginev1alpha1 "github.com/shadow-diff/monarch/api/v1alpha1"
)

func TestBeruDBSecretRef(t *testing.T) {
	cases := []struct {
		name, env, wantNS, wantName string
		wantErr                     bool
	}{
		{name: "unset", env: "", wantErr: true},
		{name: "blank", env: "   ", wantErr: true},
		{name: "bare name defaults namespace", env: "beru-postgres",
			wantNS: defaultBeruDBSecretNS, wantName: "beru-postgres"},
		{name: "qualified", env: "infra/beru-db",
			wantNS: "infra", wantName: "beru-db"},
		{name: "missing namespace half", env: "/beru-db", wantErr: true},
		{name: "missing name half", env: "infra/", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(envBeruDBSecret, tc.env)
			ns, name, err := beruDBSecretRef()
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if ns != tc.wantNS || name != tc.wantName {
				t.Fatalf("ref = %q/%q, want %q/%q", ns, name, tc.wantNS, tc.wantName)
			}
		})
	}
}

func TestSyncBeruDBSecret_requiresEnv(t *testing.T) {
	t.Setenv(envBeruDBSecret, "")
	rec := &ShadowTestReconciler{}
	err := rec.syncBeruDBSecret(context.Background(), beruLocalTestShadowTest(), "shadow-default-demo")
	if err == nil || !strings.Contains(err.Error(), "BERU_DB_SECRET is required") {
		t.Fatalf("err = %v, want BERU_DB_SECRET is required", err)
	}
}

func beruLocalTestShadowTest() *enginev1alpha1.ShadowTest {
	return &enginev1alpha1.ShadowTest{
		ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "default"},
		Status: enginev1alpha1.ShadowTestStatus{
			CurrentSessionID:         "session-1",
			CurrentReplayExecutionID: "exec-1",
		},
	}
}

func envValue(c corev1.Container, name string) (string, bool) {
	for _, e := range c.Env {
		if e.Name == name {
			return e.Value, true
		}
	}
	return "", false
}

func TestLocalBeruPodSpec_postgresAndWAL(t *testing.T) {
	t.Setenv(envBeruDBSecret, "monarch-system/beru-postgres")
	container, volumes := localBeruPodSpec(beruLocalTestShadowTest())

	if len(container.EnvFrom) != 1 {
		t.Fatalf("EnvFrom = %+v, want one secretRef", container.EnvFrom)
	}
	ref := container.EnvFrom[0].SecretRef
	if ref == nil || ref.Name != "beru-postgres" {
		t.Fatalf("secretRef = %+v, want beru-postgres", ref)
	}
	if len(volumes) != 1 || volumes[0].Name != volumeNameLocalBeruData {
		t.Fatalf("volumes = %+v, want WAL EmptyDir alongside Postgres", volumes)
	}
	if volumes[0].EmptyDir == nil || volumes[0].EmptyDir.Medium != corev1.StorageMediumDefault {
		t.Fatalf("expected a disk EmptyDir, got %+v", volumes[0].VolumeSource)
	}
	if len(container.VolumeMounts) != 1 || container.VolumeMounts[0].MountPath != "/data" {
		t.Fatalf("volume mounts = %+v", container.VolumeMounts)
	}
	if v, ok := envValue(container, "BERU_DB_PATH"); ok {
		t.Fatalf("BERU_DB_PATH = %q, want it absent", v)
	}

	if v, _ := envValue(container, envSessionID); v != "session-1" {
		t.Fatalf("%s = %q, want session-1", envSessionID, v)
	}
	if v, _ := envValue(container, envReplayExecutionID); v != "exec-1" {
		t.Fatalf("%s = %q, want exec-1", envReplayExecutionID, v)
	}
	if v, _ := envValue(container, "SHADOW_NAMESPACE"); v != "default" {
		t.Fatalf("SHADOW_NAMESPACE = %q, want default", v)
	}
}
