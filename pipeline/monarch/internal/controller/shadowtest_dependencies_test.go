package controller

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
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
				Type:            "redis",
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

	// A proxied dependency's connection string points at loopback so
	// shadow-soldier sees the traffic; the Service DNS name moves to the
	// sidecar's own route table.
	It("points proxied dependency env vars at loopback", func() {
		env := dependencyEnvVarsForRole(st, shadowNS, roleControlA)
		Expect(env).To(HaveLen(1))
		Expect(env[0].Name).To(Equal("REDIS_ADDR"))
		Expect(env[0].Value).To(Equal("127.0.0.1:6379"))
	})

	It("injects a loopback MONGO_URL for mongodb type", func() {
		mongo := &enginev1alpha1.ShadowTest{
			Spec: enginev1alpha1.ShadowTestSpec{
				Dependencies: []enginev1alpha1.DependencySpec{{
					Name: "mongo", Type: "mongodb", Image: "mongo:7", Port: 27017, EnvVarInjection: "MONGO_URL",
				}},
			},
		}
		env := dependencyEnvVarsForRole(mongo, shadowNS, roleControlA)
		Expect(env[0].Name).To(Equal("MONGO_URL"))
		Expect(env[0].Value).To(Equal("mongodb://127.0.0.1:27017"))
		Expect(env[0].Value).NotTo(ContainSubstring("tls"))
		Expect(env[0].Value).NotTo(ContainSubstring("mongodb+srv"))
	})

	It("injects a plaintext loopback DSN for postgres type", func() {
		pg := &enginev1alpha1.ShadowTest{
			Spec: enginev1alpha1.ShadowTestSpec{
				Dependencies: []enginev1alpha1.DependencySpec{{
					Name: "pg", Type: "postgres", EnvVarInjection: "PG_DSN",
				}},
			},
		}
		image, port := resolveDependencyDefaults(pg.Spec.Dependencies[0])
		Expect(image).To(Equal("postgres:16-alpine"))
		Expect(port).To(Equal(int32(5432)))

		env := dependencyEnvVarsForRole(pg, shadowNS, roleControlA)
		Expect(env[0].Value).To(Equal("postgres://postgres@127.0.0.1:5432/postgres?sslmode=disable"))

		// Without an explicit auth method the postgres image refuses to
		// initialise, so the readiness gate would never open.
		Expect(dependencyContainerEnv(pg.Spec.Dependencies[0])).To(ContainElement(corev1.EnvVar{
			Name: "POSTGRES_HOST_AUTH_METHOD", Value: "trust",
		}))
	})

	// The sidecar dials the real per-role Service; only the app is redirected.
	It("routes shadow-soldier to the per-role dependency service", func() {
		routes := soldierRoutesFor(st, shadowNS, roleCandidate)
		Expect(routes).To(HaveLen(1))
		Expect(routes[0].Protocol).To(Equal("redis"))
		Expect(routes[0].Listen).To(Equal(int32(6379)))
		Expect(routes[0].Upstream).To(Equal("redis-candidate.shadow-default-mytest.svc.cluster.local:6379"))
	})

	It("resolves rabbitmq defaults from type alone", func() {
		dep := enginev1alpha1.DependencySpec{Name: "rabbitmq", Type: "rabbitmq", EnvVarInjection: "AMQP_URL"}
		image, port := resolveDependencyDefaults(dep)
		Expect(image).To(Equal("rabbitmq:3-management-alpine"))
		Expect(port).To(Equal(int32(5672)))
		Expect(validateDependencies(&enginev1alpha1.ShadowTest{
			Spec: enginev1alpha1.ShadowTestSpec{Dependencies: []enginev1alpha1.DependencySpec{dep}},
		})).To(Succeed())
	})

	It("detects rabbitmq broker dependencies by type or env var injection", func() {
		Expect(isRabbitMQBrokerDependency(enginev1alpha1.DependencySpec{
			Name: "rabbitmq", Type: "rabbitmq", EnvVarInjection: "AMQP_URL",
		})).To(BeTrue())
		Expect(isRabbitMQBrokerDependency(enginev1alpha1.DependencySpec{
			Name: "rabbitmq", Image: "rabbitmq:3", Port: 5672, EnvVarInjection: "AMQP_URL",
		})).To(BeTrue())
		Expect(isRabbitMQBrokerDependency(enginev1alpha1.DependencySpec{
			Name: "redis", Type: "redis", Image: "redis:7", Port: 6379, EnvVarInjection: "REDIS_ADDR",
		})).To(BeFalse())
	})

	It("configures rabbitmq broker container with plugin file and firehose readiness", func() {
		dep := enginev1alpha1.DependencySpec{
			Name: "rabbitmq", Type: "rabbitmq", Image: "rabbitmq:3-management-alpine", Port: 5672, EnvVarInjection: "AMQP_URL",
		}
		image, port := resolveDependencyDefaults(dep)
		c := rabbitMQBrokerContainer(image, port)
		Expect(c.Env).To(ContainElement(corev1.EnvVar{
			Name: envRabbitMQEnabledPluginsFile, Value: rabbitmqPluginsMountPath,
		}))
		Expect(c.VolumeMounts).To(ContainElement(corev1.VolumeMount{
			Name: volumeNameRabbitMQPlugins, MountPath: rabbitmqPluginsMountPath,
			SubPath: rabbitmqPluginsConfigKey, ReadOnly: true,
		}))
		Expect(c.Lifecycle).To(BeNil())
		Expect(c.StartupProbe.Exec.Command).To(ContainElement(ContainSubstring("trace_on")))
		Expect(c.StartupProbe.Exec.Command).To(ContainElement(ContainSubstring("/tmp/.firehose_ready")))
		Expect(c.StartupProbe.TimeoutSeconds).To(Equal(int32(60)))
		Expect(c.Resources.Limits.Memory().String()).To(Equal("512Mi"))
		Expect(c.ReadinessProbe.TCPSocket).NotTo(BeNil())
		Expect(c.ReadinessProbe.TCPSocket.Port.IntValue()).To(Equal(5672))
		Expect(c.ReadinessProbe.Exec).To(BeNil())
	})

	// RabbitMQ is not proxied — its egress is already diffed by
	// egress-relay-rabbitmq off the broker Firehose — so it keeps the Service DNS
	// name and produces no shadow-soldier route.
	It("injects full amqp URL for AMQP_URL env", func() {
		rmq := &enginev1alpha1.ShadowTest{
			Spec: enginev1alpha1.ShadowTestSpec{
				Dependencies: []enginev1alpha1.DependencySpec{{
					Name: "rabbitmq", Type: "rabbitmq", Image: "rabbitmq:3", Port: 5672, EnvVarInjection: "AMQP_URL",
				}},
			},
		}
		env := dependencyEnvVarsForRole(rmq, shadowNS, roleControlA)
		Expect(env[0].Value).To(HavePrefix("amqp://guest:guest@"))
		Expect(env[0].Value).To(ContainSubstring("rabbitmq-control-a.shadow-default-mytest.svc.cluster.local:5672"))
		Expect(needsShadowSoldier(rmq)).To(BeFalse())
		Expect(soldierRoutesFor(rmq, shadowNS, roleControlA)).To(BeEmpty())
	})

	// Two proxied dependencies cannot share a loopback port: the second listener
	// would fail to bind, leaving one dependency silently unreported.
	It("rejects two proxied dependencies on the same port", func() {
		Expect(validateDependencies(&enginev1alpha1.ShadowTest{
			Spec: enginev1alpha1.ShadowTestSpec{
				Dependencies: []enginev1alpha1.DependencySpec{
					{Name: "cache", Type: "redis", Port: 6379, EnvVarInjection: "A"},
					{Name: "sessions", Type: "redis", Port: 6379, EnvVarInjection: "B"},
				},
			},
		})).NotTo(Succeed())
	})

	It("validates dependencies", func() {
		Expect(validateDependencies(st)).To(Succeed())
		Expect(validateDependencies(&enginev1alpha1.ShadowTest{
			Spec: enginev1alpha1.ShadowTestSpec{
				Dependencies: []enginev1alpha1.DependencySpec{{Name: "redis", Type: "redis", Image: "redis:7-alpine", Port: 6379}},
			},
		})).NotTo(Succeed())
		Expect(validateDependencies(&enginev1alpha1.ShadowTest{
			Spec: enginev1alpha1.ShadowTestSpec{
				Dependencies: []enginev1alpha1.DependencySpec{
					{Name: "redis", Type: "redis", Image: "redis:7-alpine", Port: 6379, EnvVarInjection: "A"},
					{Name: "redis", Type: "redis", Image: "redis:7-alpine", Port: 6379, EnvVarInjection: "B"},
				},
			},
		})).NotTo(Succeed())
	})
})
