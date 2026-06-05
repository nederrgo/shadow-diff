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

	It("injects cleartext MONGO_URL for port 27017", func() {
		mongo := &enginev1alpha1.ShadowTest{
			Spec: enginev1alpha1.ShadowTestSpec{
				Dependencies: []enginev1alpha1.DependencySpec{{
					Name: "mongo", Image: "mongo:7", Port: 27017, EnvVarInjection: "MONGO_URL",
				}},
			},
		}
		env := dependencyEnvVarsForRole(mongo, shadowNS, roleControlA)
		Expect(env[0].Name).To(Equal("MONGO_URL"))
		Expect(env[0].Value).To(Equal(shadowMongoProxyURL))
		Expect(env[0].Value).NotTo(ContainSubstring("tls"))
		Expect(env[0].Value).NotTo(ContainSubstring("mongodb+srv"))
	})

	It("injects full amqp URL for AMQP_URL env", func() {
		rmq := &enginev1alpha1.ShadowTest{
			Spec: enginev1alpha1.ShadowTestSpec{
				Dependencies: []enginev1alpha1.DependencySpec{{
					Name: "rabbitmq", Image: "rabbitmq:3", Port: 5672, EnvVarInjection: "AMQP_URL",
				}},
			},
		}
		env := dependencyEnvVarsForRole(rmq, shadowNS, roleControlA)
		Expect(env[0].Value).To(HavePrefix("amqp://guest:guest@"))
		Expect(env[0].Value).To(ContainSubstring("rabbitmq-control-a.shadow-default-mytest.svc.cluster.local:5672"))
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
