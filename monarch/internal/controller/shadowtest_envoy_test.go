package controller

import (
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	enginev1alpha1 "github.com/shadow-diff/monarch/api/v1alpha1"
)

func TestRenderEnvoyYAML(t *testing.T) {
	st := &enginev1alpha1.ShadowTest{
		ObjectMeta: metav1.ObjectMeta{Name: "test"},
		Spec: enginev1alpha1.ShadowTestSpec{
			ServicePort:     80,
			ApplicationPort: 8080,
			BeruGRPCAddress: "beru.beru-system.svc.cluster.local:50051",
			BeruGRPCTimeout: "2s",
		},
	}
	yaml, err := renderEnvoyYAML(st, roleControlA)
	if err != nil {
		t.Fatal(err)
	}
	checks := []string{
		"generate_request_id: true",
		"x-shadow-trace-id",
		"ADD_IF_ABSENT",
		"x-shadow-role",
		"value: \"control-a\"",
		"initial_metadata",
		"envoy.extensions.filters.http.ext_proc.v3.ExternalProcessor",
		"envoy.filters.http.ext_proc",
		"envoy.filters.http.header_mutation",
		"beru_ext_proc",
		"cluster: local_app",
		"port_value: 80",
		"port_value: 8080",
		"failure_mode_allow: true",
		"response_body_mode: BUFFERED",
	}
	for _, c := range checks {
		if !strings.Contains(yaml, c) {
			t.Fatalf("expected %q in envoy yaml:\n%s", c, yaml)
		}
	}
}

func TestApplicationPortFor_defaultOffset(t *testing.T) {
	st := &enginev1alpha1.ShadowTest{
		Spec: enginev1alpha1.ShadowTestSpec{ServicePort: 80},
	}
	if got := applicationPortFor(st); got != 81 {
		t.Fatalf("expected 81, got %d", got)
	}
}
