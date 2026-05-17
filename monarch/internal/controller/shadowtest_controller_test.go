package controller

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	enginev1alpha1 "github.com/shadow-diff/monarch/api/v1alpha1"
)

var _ = Describe("ShadowTest Controller", func() {
	Context("When reconciling a resource", func() {
		const resourceName = "test-resource"

		ctx := context.Background()

		typeNamespacedName := types.NamespacedName{
			Name:      resourceName,
			Namespace: "default",
		}

		BeforeEach(func() {
			targetKey := types.NamespacedName{Name: "target-app", Namespace: "default"}
			var target appsv1.Deployment
			if err := k8sClient.Get(ctx, targetKey, &target); errors.IsNotFound(err) {
				target = appsv1.Deployment{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "target-app",
						Namespace: "default",
					},
					Spec: appsv1.DeploymentSpec{
						Replicas: int32Ptr(1),
						Selector: &metav1.LabelSelector{
							MatchLabels: map[string]string{"app": "target-app"},
						},
						Template: corev1.PodTemplateSpec{
							ObjectMeta: metav1.ObjectMeta{
								Labels: map[string]string{"app": "target-app"},
							},
							Spec: corev1.PodSpec{
								Containers: []corev1.Container{
									{
										Name:  "app",
										Image: "busybox:1.36",
										Env: []corev1.EnvVar{
											{Name: "FOO", Value: "bar"},
										},
									},
								},
							},
						},
					},
				}
				Expect(k8sClient.Create(ctx, &target)).To(Succeed())
			} else {
				Expect(err).NotTo(HaveOccurred())
			}

			err := k8sClient.Get(ctx, typeNamespacedName, &enginev1alpha1.ShadowTest{})
			if err != nil && errors.IsNotFound(err) {
				st := &enginev1alpha1.ShadowTest{
					ObjectMeta: metav1.ObjectMeta{
						Name:      resourceName,
						Namespace: "default",
					},
					Spec: enginev1alpha1.ShadowTestSpec{
						TargetDeployment: "target-app",
						TargetNamespace:  "default",
						OldImage:         "busybox:1.36",
						NewImage:         "busybox:1.36",
						ServicePort:      8080,
						ApplicationPort:  8081,
					},
				}
				Expect(k8sClient.Create(ctx, st)).To(Succeed())
			}
		})

		AfterEach(func() {
			st := &enginev1alpha1.ShadowTest{}
			err := k8sClient.Get(ctx, typeNamespacedName, st)
			if err == nil {
				shadowNS := shadowNamespaceForCR(st)
				Expect(k8sClient.Delete(ctx, st)).To(Succeed())
				_ = client.IgnoreNotFound(k8sClient.Delete(ctx, &corev1.Namespace{
					ObjectMeta: metav1.ObjectMeta{Name: shadowNS},
				}))
			}

			rec := &ShadowTestReconciler{
				Client: k8sClient,
				Scheme: clientgoscheme.Scheme,
			}
			for i := 0; i < 25; i++ {
				_, _ = rec.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
				err = k8sClient.Get(ctx, typeNamespacedName, st)
				if errors.IsNotFound(err) {
					break
				}
				time.Sleep(200 * time.Millisecond)
			}
			if err := k8sClient.Get(ctx, typeNamespacedName, st); err == nil {
				patch := client.RawPatch(types.MergePatchType, []byte(`{"metadata":{"finalizers":[]}}`))
				Expect(k8sClient.Patch(ctx, st, patch)).To(Succeed())
				Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, st))).To(Succeed())
			}

			_ = client.IgnoreNotFound(k8sClient.Delete(ctx, &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{Name: "target-app", Namespace: "default"},
			}))
		})

		It("should create three shadow deployments in the dedicated namespace", func() {
			rec := &ShadowTestReconciler{
				Client: k8sClient,
				Scheme: clientgoscheme.Scheme,
			}

			for i := 0; i < 6; i++ {
				_, err := rec.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
				Expect(err).NotTo(HaveOccurred())
			}

			shadowNS := shadowNamespaceForCR(&enginev1alpha1.ShadowTest{
				ObjectMeta: metav1.ObjectMeta{Name: resourceName, Namespace: "default"},
			})

			var deps appsv1.DeploymentList
			Expect(k8sClient.List(ctx, &deps, client.InNamespace(shadowNS))).To(Succeed())
			Expect(deps.Items).To(HaveLen(3))

			roles := map[string]struct{}{}
			for _, d := range deps.Items {
				roles[d.Labels[labelRole]] = struct{}{}
			}
			Expect(roles).To(HaveKey(roleControlA))
			Expect(roles).To(HaveKey(roleControlB))
			Expect(roles).To(HaveKey(roleCandidate))

			var cms corev1.ConfigMapList
			Expect(k8sClient.List(ctx, &cms, client.InNamespace(shadowNS))).To(Succeed())
			Expect(cms.Items).To(HaveLen(3))

			for _, d := range deps.Items {
				role := d.Labels[labelRole]
				Expect(d.Spec.Template.Spec.Containers).To(HaveLen(2))
				Expect(d.Spec.Template.Spec.Containers[0].Name).To(Equal("app"))
				Expect(d.Spec.Template.Spec.Containers[1].Name).To(Equal(containerEnvoySidecar))
				Expect(d.Spec.Template.Spec.Containers[1].Image).To(Equal(envoyImage))
				Expect(d.Spec.Template.Spec.Containers[1].ImagePullPolicy).To(Equal(envoyImagePullPolicy))
				Expect(d.Spec.Template.Spec.Containers[1].Env).To(ContainElement(corev1.EnvVar{
					Name:  envShadowRole,
					Value: role,
				}))

				expectedCM := envoyConfigMapName(&enginev1alpha1.ShadowTest{
					ObjectMeta: metav1.ObjectMeta{Name: resourceName, Namespace: "default"},
				}, role)
				Expect(d.Spec.Template.Spec.Volumes).To(HaveLen(1))
				Expect(d.Spec.Template.Spec.Volumes[0].Name).To(Equal(volumeNameEnvoyConfig))
				Expect(d.Spec.Template.Spec.Volumes[0].ConfigMap).NotTo(BeNil())
				Expect(d.Spec.Template.Spec.Volumes[0].ConfigMap.Name).To(Equal(expectedCM))
				Expect(d.Spec.Template.Spec.Containers[1].VolumeMounts).To(ContainElement(corev1.VolumeMount{
					Name:      volumeNameEnvoyConfig,
					MountPath: "/etc/envoy",
					ReadOnly:  true,
				}))

				var cm corev1.ConfigMap
				Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: shadowNS, Name: expectedCM}, &cm)).To(Succeed())
				Expect(cm.Data[configMapKeyEnvoyYAML]).NotTo(BeEmpty())
				Expect(cm.Data[configMapKeyEnvoyYAML]).To(ContainSubstring("generate_request_id: true"))
				Expect(cm.Data[configMapKeyEnvoyYAML]).To(ContainSubstring("x-shadow-trace-id"))
				Expect(cm.Data[configMapKeyEnvoyYAML]).To(ContainSubstring("envoy.filters.http.ext_proc"))
				Expect(d.Spec.Template.Spec.Containers[0].Ports[0].ContainerPort).To(Equal(int32(8081)))
			}
		})
	})
})

func int32Ptr(v int32) *int32 {
	return &v
}
