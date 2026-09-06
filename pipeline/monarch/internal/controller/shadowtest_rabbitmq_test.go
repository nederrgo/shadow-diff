package controller

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	enginev1alpha1 "github.com/shadow-diff/monarch/api/v1alpha1"
)

const testAMQPCredsSecretName = "rmq-prod-creds"

func testAMQPCredentialsRef() *corev1.LocalObjectReference {
	return &corev1.LocalObjectReference{Name: testAMQPCredsSecretName}
}

func testAMQPCredentialsSecret(ns string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: testAMQPCredsSecretName, Namespace: ns},
		Type:       corev1.SecretTypeOpaque,
		Data: map[string][]byte{
			secretKeyAMQPUsername: []byte("guest"),
			secretKeyAMQPPassword: []byte("guest"),
		},
	}
}

func TestParseHostOnlyAMQPURL(t *testing.T) {
	t.Parallel()
	if _, err := parseHostOnlyAMQPURL("amqp://prod:5672"); err != nil {
		t.Fatalf("host-only: %v", err)
	}
	if _, err := parseHostOnlyAMQPURL("amqp://guest:guest@prod:5672"); err == nil || !strings.Contains(err.Error(), "credentialsSecretRef") {
		t.Fatalf("userinfo err = %v", err)
	}
	if _, err := parseHostOnlyAMQPURL("http://prod:5672"); err == nil {
		t.Fatal("want scheme error")
	}
}

func TestResolveProdAMQPURL(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	_ = enginev1alpha1.AddToScheme(scheme)

	st := &enginev1alpha1.ShadowTest{
		ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "default"},
	}
	spec := &enginev1alpha1.AMQPInputSpec{
		ProdURL:              "amqp://prod:5672/vhost",
		CredentialsSecretRef: testAMQPCredentialsRef(),
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(testAMQPCredentialsSecret("default")).Build()
	rec := &ShadowTestReconciler{Client: c}

	got, err := rec.resolveProdAMQPURL(context.Background(), st, spec)
	if err != nil {
		t.Fatal(err)
	}
	if got != "amqp://guest:guest@prod:5672/vhost" {
		t.Fatalf("dsn = %q", got)
	}

	if _, err := rec.resolveProdAMQPURL(context.Background(), st, &enginev1alpha1.AMQPInputSpec{
		ProdURL: "amqp://prod:5672",
	}); err == nil || !strings.Contains(err.Error(), "credentialsSecretRef") {
		t.Fatalf("empty ref err = %v", err)
	}

	if _, err := rec.resolveProdAMQPURL(context.Background(), st, &enginev1alpha1.AMQPInputSpec{
		ProdURL:              "amqp://user:pass@prod:5672",
		CredentialsSecretRef: testAMQPCredentialsRef(),
	}); err == nil || !strings.Contains(err.Error(), "must not include credentials") {
		t.Fatalf("userinfo err = %v", err)
	}

	missingKeys := testAMQPCredentialsSecret("default")
	missingKeys.Name = "rmq-prod-creds-nokeys"
	missingKeys.Data = map[string][]byte{}
	c2 := fake.NewClientBuilder().WithScheme(scheme).WithObjects(missingKeys).Build()
	rec2 := &ShadowTestReconciler{Client: c2}
	if _, err := rec2.resolveProdAMQPURL(context.Background(), st, &enginev1alpha1.AMQPInputSpec{
		ProdURL:              "amqp://prod:5672",
		CredentialsSecretRef: &corev1.LocalObjectReference{Name: missingKeys.Name},
	}); err == nil || !strings.Contains(err.Error(), secretKeyAMQPUsername) {
		t.Fatalf("missing keys err = %v", err)
	}
}
