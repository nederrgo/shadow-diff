package controller

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	enginev1alpha1 "github.com/shadow-diff/monarch/api/v1alpha1"
)

var _ = Describe("shadow dependencies", func() {
	const shadowNS = "shadow-default-mytest"

	st := &enginev1alpha1.ShadowTest{
		ObjectMeta: metav1.ObjectMeta{Name: "mytest", Namespace: "default"},
		Spec: enginev1alpha1.ShadowTestSpec{
			Dependencies: []enginev1alpha1.DependencySpec{{
				Name:            "redis",
				Image:           "redis:7-alpine",
				Port:            6379,
				EnvVarInjection: "REDIS_ADDR",
			}},
		},
	}

	It("names dependency resources per role", func() {
		Expect(dependencyResourceName("redis", roleControlA)).To(Equal("redis-control-a"))
		Expect(dependencyResourceName("redis", roleControlB)).To(Equal("redis-control-b"))
		Expect(dependencyResourceName("redis", roleCandidate)).To(Equal("redis-candidate"))
	})

	It("builds host:port endpoints per role", func() {
		Expect(dependencyEndpoint(shadowNS, "redis", roleControlA, 6379)).
			To(Equal("redis-control-a.shadow-default-mytest.svc.cluster.local:6379"))
		Expect(dependencyEndpoint(shadowNS, "redis", roleControlB, 6379)).
			To(Equal("redis-control-b.shadow-default-mytest.svc.cluster.local:6379"))
		Expect(dependencyEndpoint(shadowNS, "redis", roleCandidate, 6379)).
			To(Equal("redis-candidate.shadow-default-mytest.svc.cluster.local:6379"))
	})

	It("injects role-specific env vars after target env", func() {
		env := dependencyEnvVarsForRole(st, shadowNS, roleControlA)
		Expect(env).To(HaveLen(1))
		Expect(env[0].Name).To(Equal("REDIS_ADDR"))
		Expect(env[0].Value).To(Equal("redis-control-a.shadow-default-mytest.svc.cluster.local:6379"))
	})

	It("validates dependencies", func() {
		Expect(validateDependencies(st)).To(Succeed())
		Expect(validateDependencies(&enginev1alpha1.ShadowTest{
			Spec: enginev1alpha1.ShadowTestSpec{
				Dependencies: []enginev1alpha1.DependencySpec{{Name: "redis", Image: "redis:7-alpine", Port: 6379}},
			},
		})).NotTo(Succeed())
		Expect(validateDependencies(&enginev1alpha1.ShadowTest{
			Spec: enginev1alpha1.ShadowTestSpec{
				Dependencies: []enginev1alpha1.DependencySpec{
					{Name: "redis", Image: "redis:7-alpine", Port: 6379, EnvVarInjection: "A"},
					{Name: "redis", Image: "redis:7-alpine", Port: 6379, EnvVarInjection: "B"},
				},
			},
		})).NotTo(Succeed())
	})
})
