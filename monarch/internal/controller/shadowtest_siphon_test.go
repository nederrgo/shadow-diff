package controller

import (
	"testing"

	enginev1alpha1 "github.com/shadow-diff/monarch/api/v1alpha1"
)

func TestBuildSiphonTarget(t *testing.T) {
	st := &enginev1alpha1.ShadowTest{}
	st.Namespace = "default"
	st.Name = "my-st"
	st.Spec.ServicePort = 8080

	target := buildSiphonTarget(st, "shadow-default-my-st", []string{"10.244.1.2"})
	if target.ShadowTest != "default/my-st" {
		t.Fatalf("shadowtest id %q", target.ShadowTest)
	}
	if len(target.TargetIPs) != 1 || target.TargetIPs[0] != "10.244.1.2" {
		t.Fatalf("ips %v", target.TargetIPs)
	}
	if target.IgrisHost == "" {
		t.Fatal("igris host empty")
	}
	if len(target.Listeners) == 0 {
		t.Fatal("expected default listener")
	}
}
