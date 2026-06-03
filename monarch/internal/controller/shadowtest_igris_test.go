package controller

import (
	"encoding/json"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	enginev1alpha1 "github.com/shadow-diff/monarch/api/v1alpha1"
)

func TestIgrisListenersJSONDefault(t *testing.T) {
	t.Parallel()
	st := &enginev1alpha1.ShadowTest{
		Spec: enginev1alpha1.ShadowTestSpec{ServicePort: 80},
	}
	got, err := igrisListenersJSON(st)
	if err != nil {
		t.Fatal(err)
	}
	var listeners []struct {
		Port   int32  `json:"port"`
		Driver string `json:"driver"`
	}
	if err := json.Unmarshal([]byte(got), &listeners); err != nil {
		t.Fatal(err)
	}
	if len(listeners) != 1 || listeners[0].Port != 80 || listeners[0].Driver != "http_request" {
		t.Fatalf("got %s", got)
	}
}

func TestIgrisListenersJSONFromInputs(t *testing.T) {
	t.Parallel()
	st := &enginev1alpha1.ShadowTest{
		Spec: enginev1alpha1.ShadowTestSpec{
			ServicePort: 80,
			Inputs: []enginev1alpha1.InputSpec{
				{Port: 80, Driver: "http_request"},
				{Port: 27017, Driver: "tcp_stream"},
			},
		},
	}
	got, err := igrisListenersJSON(st)
	if err != nil {
		t.Fatal(err)
	}
	var listeners []struct {
		Port int32 `json:"port"`
	}
	if err := json.Unmarshal([]byte(got), &listeners); err != nil {
		t.Fatal(err)
	}
	if len(listeners) != 2 {
		t.Fatalf("got %s", got)
	}
}

func TestIgrisControlURLs(t *testing.T) {
	t.Parallel()
	st := &enginev1alpha1.ShadowTest{
		ObjectMeta: testObjectMeta("my-app"),
		Spec:       enginev1alpha1.ShadowTestSpec{ServicePort: 8080},
	}
	ns := "shadow-default-my-app"
	a, b, c := igrisControlURLs(st, ns)
	wantA := "http://my-app-control-a.shadow-default-my-app.svc.cluster.local:8080"
	if a != wantA {
		t.Fatalf("control-a URL = %q want %q", a, wantA)
	}
	if b == "" || c == "" {
		t.Fatal("expected non-empty URLs")
	}
}

func TestIgrisControlHosts(t *testing.T) {
	t.Parallel()
	st := &enginev1alpha1.ShadowTest{
		ObjectMeta: testObjectMeta("my-app"),
		Spec:       enginev1alpha1.ShadowTestSpec{ServicePort: 8080},
	}
	ns := "shadow-default-my-app"
	a, b, c := igrisControlHosts(st, ns)
	want := "my-app-control-a.shadow-default-my-app.svc.cluster.local"
	if a != want {
		t.Fatalf("control-a host = %q want %q", a, want)
	}
	if b == "" || c == "" {
		t.Fatal("expected non-empty hosts")
	}
}

func testObjectMeta(name string) metav1.ObjectMeta {
	return metav1.ObjectMeta{Name: name, Namespace: "default"}
}
