package controller

import (
	"os"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	enginev1alpha1 "github.com/shadow-diff/monarch/api/v1alpha1"
)

// Run with: DUMP_ENVOY=1 go test ./internal/controller -run TestDumpMongoEnvoyYAML -count=1
func TestDumpMongoEnvoyYAML(t *testing.T) {
	if os.Getenv("DUMP_ENVOY") != "1" {
		t.Skip("set DUMP_ENVOY=1 to write /tmp/mongo-envoy-*.yaml")
	}
	st := &enginev1alpha1.ShadowTest{
		ObjectMeta: metav1.ObjectMeta{Name: "mongo-test-shadow"},
		Spec: enginev1alpha1.ShadowTestSpec{
			ServicePort:     8888,
			ApplicationPort: 8080,
			BeruGRPCAddress: "beru.beru-system.svc.cluster.local:50051",
			BeruGRPCTimeout: "2s",
			Dependencies: []enginev1alpha1.DependencySpec{{
				Name: "mongo", Type: "mongodb", Image: "mongo:7", Port: 27017, EnvVarInjection: "MONGO_URL",
			}},
		},
	}
	shadowNS := "shadow-default-mongo-test-shadow"
	for _, role := range []string{roleControlA, roleControlB, roleCandidate} {
		yaml, err := renderEnvoyYAML(st, shadowNS, role)
		if err != nil {
			t.Fatal(err)
		}
		path := "/tmp/mongo-envoy-" + role + ".yaml"
		if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote %s", path)
	}
}
