package controller

import (
	"encoding/json"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	enginev1alpha1 "github.com/shadow-diff/monarch/api/v1alpha1"
)

const soldierShadowNS = "shadow-default-my-test"

func soldierShadowTest(deps ...enginev1alpha1.DependencySpec) *enginev1alpha1.ShadowTest {
	return &enginev1alpha1.ShadowTest{
		ObjectMeta: metav1.ObjectMeta{Name: "my-test", Namespace: "default"},
		Spec:       enginev1alpha1.ShadowTestSpec{Dependencies: deps},
	}
}

func envMap(c *corev1.Container) map[string]string {
	out := make(map[string]string, len(c.Env))
	for _, e := range c.Env {
		out[e.Name] = e.Value
	}
	return out
}

func TestShadowSoldierContainer(t *testing.T) {
	t.Parallel()
	st := soldierShadowTest(
		enginev1alpha1.DependencySpec{Name: "mongo", Type: "mongodb", EnvVarInjection: "MONGO_URL"},
		enginev1alpha1.DependencySpec{Name: "cache", Type: "redis", EnvVarInjection: "REDIS_ADDR"},
	)

	c, err := shadowSoldierContainer(st, soldierShadowNS, roleCandidate)
	if err != nil {
		t.Fatalf("shadowSoldierContainer: %v", err)
	}
	if c == nil {
		t.Fatal("container = nil, want a sidecar for mongodb + redis")
	}
	if c.Name != containerShadowSoldier {
		t.Fatalf("Name = %q want %q", c.Name, containerShadowSoldier)
	}

	env := envMap(c)
	if env[envShadowRole] != roleCandidate {
		t.Fatalf("SHADOW_ROLE = %q want %q", env[envShadowRole], roleCandidate)
	}
	if env[envShadowTestName] != "my-test" {
		t.Fatalf("SHADOW_TEST_NAME = %q", env[envShadowTestName])
	}

	// Reporting must not use 8080: the pod's iptables rules REDIRECT outbound
	// 8080 into Envoy's egress listener, which answers 502.
	wantBeru := "http://beru-local." + soldierShadowNS + ".svc.cluster.local:8081"
	if env[envBeruHTTPURL] != wantBeru {
		t.Fatalf("BERU_HTTP_URL = %q want %q", env[envBeruHTTPURL], wantBeru)
	}

	var routes []soldierRoute
	if err := json.Unmarshal([]byte(env[envSoldierRoutes]), &routes); err != nil {
		t.Fatalf("SOLDIER_ROUTES is not valid JSON: %v", err)
	}
	if len(routes) != 2 {
		t.Fatalf("got %d routes, want 2: %+v", len(routes), routes)
	}
	want := map[string]soldierRoute{
		"mongodb": {Protocol: "mongodb", Listen: 27017, Upstream: "mongo-candidate." + soldierShadowNS + ".svc.cluster.local:27017"},
		"redis":   {Protocol: "redis", Listen: 6379, Upstream: "cache-candidate." + soldierShadowNS + ".svc.cluster.local:6379"},
	}
	for _, got := range routes {
		if got != want[got.Protocol] {
			t.Fatalf("route %+v want %+v", got, want[got.Protocol])
		}
	}
}

// POD_NAME must come from the downward API — a literal value would be identical
// on all three roles and useless for debugging.
func TestShadowSoldierContainerUsesDownwardAPIForPodName(t *testing.T) {
	t.Parallel()
	st := soldierShadowTest(enginev1alpha1.DependencySpec{Name: "mongo", Type: "mongodb", EnvVarInjection: "MONGO_URL"})
	c, err := shadowSoldierContainer(st, soldierShadowNS, roleControlA)
	if err != nil || c == nil {
		t.Fatalf("shadowSoldierContainer = %v, %v", c, err)
	}
	for _, e := range c.Env {
		if e.Name != envPodName {
			continue
		}
		if e.ValueFrom == nil || e.ValueFrom.FieldRef == nil || e.ValueFrom.FieldRef.FieldPath != "metadata.name" {
			t.Fatalf("POD_NAME = %+v, want a metadata.name fieldRef", e)
		}
		return
	}
	t.Fatal("POD_NAME env var is missing")
}

// A ShadowTest with nothing to proxy keeps its two-container pod.
func TestShadowSoldierContainerAbsentWithoutProxiedDependency(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		st   *enginev1alpha1.ShadowTest
	}{
		{"no dependencies", soldierShadowTest()},
		{"rabbitmq only", soldierShadowTest(enginev1alpha1.DependencySpec{
			Name: "rabbitmq", Type: "rabbitmq", EnvVarInjection: "AMQP_URL",
		})},
	}
	for _, tc := range tests {
		if needsShadowSoldier(tc.st) {
			t.Fatalf("%s: needsShadowSoldier = true, want false", tc.name)
		}
		c, err := shadowSoldierContainer(tc.st, soldierShadowNS, roleControlA)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if c != nil {
			t.Fatalf("%s: got a sidecar, want none", tc.name)
		}
	}
}

func TestSoldierProtocolFor(t *testing.T) {
	t.Parallel()
	tests := []struct {
		depType string
		want    string
		proxied bool
	}{
		{"mongodb", "mongodb", true},
		{"mongo", "mongodb", true},
		{"Redis", "redis", true},
		{"postgres", "postgresql", true},
		{"postgresql", "postgresql", true},
		{"mssql", "mssql", true},
		{"sqlserver", "mssql", true},
		{"rabbitmq", "", false},
		{"kafka", "", false},
	}
	for _, tc := range tests {
		got, ok := soldierProtocolFor(enginev1alpha1.DependencySpec{Type: tc.depType})
		if ok != tc.proxied || got != tc.want {
			t.Fatalf("soldierProtocolFor(%q) = (%q, %v) want (%q, %v)", tc.depType, got, ok, tc.want, tc.proxied)
		}
	}
}

func TestShadowSoldierImageFor(t *testing.T) {
	t.Parallel()
	st := soldierShadowTest()
	if got := shadowSoldierImageFor(st); got != "shadow-soldier"+monarchImageTagSuffix() {
		t.Fatalf("default image = %q", got)
	}
	st.Spec.ShadowSoldier = &enginev1alpha1.ShadowSoldierSpec{Image: "ghcr.io/acme/soldier:1.2.3"}
	if got := shadowSoldierImageFor(st); got != "ghcr.io/acme/soldier:1.2.3" {
		t.Fatalf("CR override image = %q", got)
	}
}
