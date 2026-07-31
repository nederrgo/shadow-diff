package controller

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	enginev1alpha1 "github.com/shadow-diff/monarch/api/v1alpha1"
)

func TestShopEnv_beruHTTPURL(t *testing.T) {
	t.Parallel()
	st := &enginev1alpha1.ShadowTest{
		ObjectMeta: metav1.ObjectMeta{Name: "my-test", Namespace: "default"},
		Spec:       enginev1alpha1.ShadowTestSpec{},
	}
	shadowNS := "shadow-default-my-test"
	wantBeru := "http://" + beruHTTPHostFor(st, shadowNS)
	// Mirror reconcileShop env construction for unit coverage without a fake client.
	envs := map[string]string{
		envShopGRPCAddr:   ":50051",
		envShopHTTPAddr:   ":8080",
		envBeruHTTPURL:    wantBeru,
		envShadowTestName: st.Name,
	}
	if envs[envBeruHTTPURL] != "http://beru-local.shadow-default-my-test.svc.cluster.local:8080" {
		t.Fatalf("BERU_HTTP_URL = %q", envs[envBeruHTTPURL])
	}
	if envs[envShadowTestName] != "my-test" {
		t.Fatalf("SHADOW_TEST_NAME = %q", envs[envShadowTestName])
	}
}
