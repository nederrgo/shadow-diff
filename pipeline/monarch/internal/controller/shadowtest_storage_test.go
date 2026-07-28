package controller

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	enginev1alpha1 "github.com/shadow-diff/monarch/api/v1alpha1"
)

func TestValidateStorage(t *testing.T) {
	t.Parallel()

	if err := validateStorage(&enginev1alpha1.ShadowTest{}); err == nil {
		t.Fatal("nil storage: expected error")
	}

	ok := &enginev1alpha1.ShadowTest{
		Spec: enginev1alpha1.ShadowTestSpec{
			Storage: &enginev1alpha1.StorageConfig{
				Type:            "s3",
				BucketName:      "shadow-diff-local",
				RetentionPolicy: "Retain",
				CredentialsSecretRef: &corev1.LocalObjectReference{
					Name: "shadow-diff-s3",
				},
			},
		},
	}
	if err := validateStorage(ok); err != nil {
		t.Fatalf("valid storage: %v", err)
	}

	cases := []struct {
		name string
		cfg  *enginev1alpha1.StorageConfig
	}{
		{name: "empty bucket", cfg: &enginev1alpha1.StorageConfig{Type: "s3"}},
		{name: "empty type", cfg: &enginev1alpha1.StorageConfig{BucketName: "b"}},
		{name: "bad type", cfg: &enginev1alpha1.StorageConfig{Type: "gcs", BucketName: "b"}},
		{name: "bad retention", cfg: &enginev1alpha1.StorageConfig{Type: "s3", BucketName: "b", RetentionPolicy: "Forever"}},
		{name: "empty secret name", cfg: &enginev1alpha1.StorageConfig{
			Type:                 "s3",
			BucketName:           "b",
			CredentialsSecretRef: &corev1.LocalObjectReference{Name: ""},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := validateStorage(&enginev1alpha1.ShadowTest{
				Spec: enginev1alpha1.ShadowTestSpec{Storage: tc.cfg},
			})
			if err == nil {
				t.Fatal("expected error")
			}
		})
	}

	t.Run("bad mode", func(t *testing.T) {
		t.Parallel()
		err := validateStorage(&enginev1alpha1.ShadowTest{
			Spec: enginev1alpha1.ShadowTestSpec{
				Mode:    "live",
				Storage: &enginev1alpha1.StorageConfig{Type: "s3", BucketName: "b"},
			},
		})
		if err == nil {
			t.Fatal("expected error for live mode")
		}
	})

	t.Run("replay without session", func(t *testing.T) {
		t.Parallel()
		err := validateStorage(&enginev1alpha1.ShadowTest{
			Spec: enginev1alpha1.ShadowTestSpec{
				Mode:    modeReplay,
				Storage: &enginev1alpha1.StorageConfig{Type: "s3", BucketName: "b"},
			},
		})
		if err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("replay with status session", func(t *testing.T) {
		t.Parallel()
		err := validateStorage(&enginev1alpha1.ShadowTest{
			Spec: enginev1alpha1.ShadowTestSpec{
				Mode:    modeReplay,
				Storage: &enginev1alpha1.StorageConfig{Type: "s3", BucketName: "b"},
			},
			Status: enginev1alpha1.ShadowTestStatus{CurrentSessionID: "session-1"},
		})
		if err != nil {
			t.Fatalf("unexpected: %v", err)
		}
	})
}

func TestOperatingMode(t *testing.T) {
	t.Parallel()
	if got := operatingMode(&enginev1alpha1.ShadowTest{}); got != modeRecord {
		t.Fatalf("empty mode = %q, want record", got)
	}
	if got := operatingMode(&enginev1alpha1.ShadowTest{
		Spec: enginev1alpha1.ShadowTestSpec{Mode: "REPLAY"},
	}); got != modeReplay {
		t.Fatalf("REPLAY = %q, want replay", got)
	}
}

func TestStorageEnvVars(t *testing.T) {
	t.Parallel()
	st := &enginev1alpha1.ShadowTest{
		ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "ns"},
		Spec: enginev1alpha1.ShadowTestSpec{
			Mode: modeRecord,
			Storage: &enginev1alpha1.StorageConfig{
				Type:       "s3",
				BucketName: "bucket",
				Endpoint:   "http://minio:9000",
				Region:     "us-east-1",
				CredentialsSecretRef: &corev1.LocalObjectReference{
					Name: "shadow-diff-s3",
				},
			},
		},
	}
	env := storageEnvVars(st, "session-9")
	byName := map[string]corev1.EnvVar{}
	for _, e := range env {
		byName[e.Name] = e
	}
	for _, name := range []string{
		envOperatingMode, envS3Bucket, envS3Endpoint, envS3Region,
		envTestNamespace, envTestName, envSessionID,
		envAWSAccessKey, envAWSSecretKey,
	} {
		if _, ok := byName[name]; !ok {
			t.Fatalf("missing env %s", name)
		}
	}
	if byName[envOperatingMode].Value != modeRecord {
		t.Fatalf("OPERATING_MODE = %q", byName[envOperatingMode].Value)
	}
	if byName[envSessionID].Value != "session-9" {
		t.Fatalf("SESSION_ID = %q", byName[envSessionID].Value)
	}
	if byName[envAWSAccessKey].ValueFrom == nil || byName[envAWSAccessKey].ValueFrom.SecretKeyRef == nil {
		t.Fatal("expected secretKeyRef for AWS_ACCESS_KEY_ID")
	}
	if byName[envAWSAccessKey].ValueFrom.SecretKeyRef.Name != "shadow-diff-s3" {
		t.Fatalf("secret name = %q", byName[envAWSAccessKey].ValueFrom.SecretKeyRef.Name)
	}
}
