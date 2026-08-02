package controller

import (
	"context"
	"strconv"
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

			secKey := types.NamespacedName{Name: "shadow-diff-s3", Namespace: "default"}
			var sec corev1.Secret
			if err := k8sClient.Get(ctx, secKey, &sec); errors.IsNotFound(err) {
				sec = corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{Name: "shadow-diff-s3", Namespace: "default"},
					Type:       corev1.SecretTypeOpaque,
					Data: map[string][]byte{
						"AWS_ACCESS_KEY_ID":     []byte("minio"),
						"AWS_SECRET_ACCESS_KEY": []byte("minio123"),
					},
				}
				Expect(k8sClient.Create(ctx, &sec)).To(Succeed())
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
						Mode:             modeRecord,
						Storage: &enginev1alpha1.StorageConfig{
							Type:       "s3",
							BucketName: "shadow-diff-local",
							Endpoint:   "http://minio:9000",
							Region:     "us-east-1",
							CredentialsSecretRef: &corev1.LocalObjectReference{
								Name: "shadow-diff-s3",
							},
						},
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
			_ = client.IgnoreNotFound(k8sClient.Delete(ctx, &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: "shadow-diff-s3", Namespace: "default"},
			}))
		})

		It("record mode creates Kaisel/Igris/Shop without ABC deployments", func() {
			rec := &ShadowTestReconciler{
				Client: k8sClient,
				Scheme: clientgoscheme.Scheme,
			}

			st := &enginev1alpha1.ShadowTest{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, st)).To(Succeed())
			shadowNS := shadowNamespaceForCR(st)

			markDeploymentAvailable := func(name string) {
				var deploy appsv1.Deployment
				if err := k8sClient.Get(ctx, types.NamespacedName{Namespace: shadowNS, Name: name}, &deploy); err != nil {
					return
				}
				if deploy.Status.AvailableReplicas >= 1 {
					return
				}
				deploy.Status.AvailableReplicas = 1
				deploy.Status.ReadyReplicas = 1
				deploy.Status.Replicas = 1
				Expect(k8sClient.Status().Update(ctx, &deploy)).To(Succeed())
			}

			for i := 0; i < 20; i++ {
				_, err := rec.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
				Expect(err).NotTo(HaveOccurred())
				Expect(k8sClient.Get(ctx, typeNamespacedName, st)).To(Succeed())
				markDeploymentAvailable(localBeruName)
				markDeploymentAvailable(igrisDeploymentName(st))
				markDeploymentAvailable(shopServiceName())
			}

			Expect(k8sClient.Get(ctx, typeNamespacedName, st)).To(Succeed())
			Expect(st.Status.CurrentSessionID).NotTo(BeEmpty())
			Expect(st.Status.Phase).To(Equal("Ready"))

			var deps appsv1.DeploymentList
			Expect(k8sClient.List(ctx, &deps, client.InNamespace(shadowNS))).To(Succeed())
			Expect(deps.Items).To(HaveLen(3)) // beru-local, igris, shop

			for _, d := range deps.Items {
				Expect(d.Labels[labelRole]).To(BeEmpty())
			}

			var igrisDeploy appsv1.Deployment
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Namespace: shadowNS,
				Name:      igrisDeploymentName(st),
			}, &igrisDeploy)).To(Succeed())
			var envNames []string
			for _, e := range igrisDeploy.Spec.Template.Spec.Containers[0].Env {
				envNames = append(envNames, e.Name)
			}
			Expect(envNames).To(ContainElements(
				envControlAURL, envControlBURL, envCandidateURL,
				envIgrisAdminAddr, envOperatingMode, envS3Bucket, envSessionID,
			))
			Expect(igrisDeploy.Spec.Template.Spec.Containers[0].Env).To(ContainElement(corev1.EnvVar{
				Name:  envOperatingMode,
				Value: modeRecord,
			}))
			Expect(igrisDeploy.Spec.Template.Spec.Containers[0].Env).To(ContainElement(corev1.EnvVar{
				Name:  envIgrisMaxConcurrency,
				Value: strconv.Itoa(igrisMaxConcurrencyFor(st)),
			}))

			var adminPort bool
			for _, p := range igrisDeploy.Spec.Template.Spec.Containers[0].Ports {
				if p.ContainerPort == igrisAdminPort {
					adminPort = true
				}
			}
			Expect(adminPort).To(BeTrue())

			var igrisSvc corev1.Service
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Namespace: shadowNS,
				Name:      igrisServiceName(st),
			}, &igrisSvc)).To(Succeed())
			Expect(igrisSvc.Spec.Ports).To(ContainElement(HaveField("Port", igrisAdminPort)))

			var shopDeploy appsv1.Deployment
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Namespace: shadowNS,
				Name:      shopServiceName(),
			}, &shopDeploy)).To(Succeed())
			Expect(shopDeploy.Spec.Template.Spec.Containers[0].Env).To(ContainElement(corev1.EnvVar{
				Name:  envOperatingMode,
				Value: modeRecord,
			}))

			var synced corev1.Secret
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Namespace: shadowNS,
				Name:      "shadow-diff-s3",
			}, &synced)).To(Succeed())

			var rule enginev1alpha1.KaiselRule
			Expect(k8sClient.Get(ctx, kaiselRuleKey(st), &rule)).To(Succeed())
		})

	})

	Context("When reconciling replay mode", func() {
		const resourceName = "test-resource-replay"
		ctx := context.Background()
		typeNamespacedName := types.NamespacedName{Name: resourceName, Namespace: "default"}

		BeforeEach(func() {
			targetKey := types.NamespacedName{Name: "target-app", Namespace: "default"}
			var target appsv1.Deployment
			if err := k8sClient.Get(ctx, targetKey, &target); errors.IsNotFound(err) {
				target = appsv1.Deployment{
					ObjectMeta: metav1.ObjectMeta{Name: "target-app", Namespace: "default"},
					Spec: appsv1.DeploymentSpec{
						Replicas: int32Ptr(1),
						Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "target-app"}},
						Template: corev1.PodTemplateSpec{
							ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "target-app"}},
							Spec: corev1.PodSpec{Containers: []corev1.Container{{
								Name:  "app",
								Image: "busybox:1.36",
								Env:   []corev1.EnvVar{{Name: "FOO", Value: "bar"}},
							}}},
						},
					},
				}
				Expect(k8sClient.Create(ctx, &target)).To(Succeed())
			}

			secKey := types.NamespacedName{Name: "shadow-diff-s3", Namespace: "default"}
			var sec corev1.Secret
			if err := k8sClient.Get(ctx, secKey, &sec); errors.IsNotFound(err) {
				Expect(k8sClient.Create(ctx, &corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{Name: "shadow-diff-s3", Namespace: "default"},
					Data: map[string][]byte{
						"AWS_ACCESS_KEY_ID":     []byte("minio"),
						"AWS_SECRET_ACCESS_KEY": []byte("minio123"),
					},
				})).To(Succeed())
			}

			st := &enginev1alpha1.ShadowTest{
				ObjectMeta: metav1.ObjectMeta{Name: resourceName, Namespace: "default"},
				Spec: enginev1alpha1.ShadowTestSpec{
					TargetDeployment: "target-app",
					TargetNamespace:  "default",
					OldImage:         "busybox:1.36",
					NewImage:         "busybox:1.36",
					ServicePort:      8080,
					ApplicationPort:  8081,
					Mode:             modeReplay,
					SessionID:        "session-replay-1",
					Storage: &enginev1alpha1.StorageConfig{
						Type:       "s3",
						BucketName: "shadow-diff-local",
						Endpoint:   "http://minio:9000",
						Region:     "us-east-1",
						CredentialsSecretRef: &corev1.LocalObjectReference{
							Name: "shadow-diff-s3",
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, st)).To(Succeed())

			Expect(k8sClient.Create(ctx, &enginev1alpha1.KaiselRule{
				ObjectMeta: metav1.ObjectMeta{
					Name:      kaiselRuleName(st),
					Namespace: st.Namespace,
				},
				Spec: enginev1alpha1.KaiselRuleSpec{TargetIPs: []string{"10.0.0.1"}},
			})).To(Succeed())
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
			rec := &ShadowTestReconciler{Client: k8sClient, Scheme: clientgoscheme.Scheme}
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
			_ = client.IgnoreNotFound(k8sClient.Delete(ctx, &enginev1alpha1.KaiselRule{
				ObjectMeta: metav1.ObjectMeta{Name: kaiselRuleName(&enginev1alpha1.ShadowTest{
					ObjectMeta: metav1.ObjectMeta{Name: resourceName, Namespace: "default"},
				}), Namespace: "default"},
			}))
		})

		It("creates ABC, deletes KaiselRule, and sets replayState started", func() {
			var startedURLs []string
			rec := &ShadowTestReconciler{
				Client: k8sClient,
				Scheme: clientgoscheme.Scheme,
				ReplayStarter: func(ctx context.Context, url string) (int, error) {
					startedURLs = append(startedURLs, url)
					return 202, nil
				},
			}
			st := &enginev1alpha1.ShadowTest{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, st)).To(Succeed())
			shadowNS := shadowNamespaceForCR(st)

			markDeploymentRollReady := func(name string) {
				var deploy appsv1.Deployment
				if err := k8sClient.Get(ctx, types.NamespacedName{Namespace: shadowNS, Name: name}, &deploy); err != nil {
					return
				}
				if deploy.Status.ReadyReplicas >= 1 && deploy.Status.UpdatedReplicas == deploy.Status.Replicas && deploy.Status.Replicas >= 1 {
					return
				}
				deploy.Status.AvailableReplicas = 1
				deploy.Status.ReadyReplicas = 1
				deploy.Status.Replicas = 1
				deploy.Status.UpdatedReplicas = 1
				Expect(k8sClient.Status().Update(ctx, &deploy)).To(Succeed())
			}

			for i := 0; i < 25; i++ {
				_, err := rec.Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
				Expect(err).NotTo(HaveOccurred())
				Expect(k8sClient.Get(ctx, typeNamespacedName, st)).To(Succeed())
				markDeploymentRollReady(localBeruName)
				markDeploymentRollReady(igrisDeploymentName(st))
				markDeploymentRollReady(shopServiceName())
				for _, role := range []string{roleControlA, roleControlB, roleCandidate} {
					markDeploymentRollReady(shadowDeploymentName(st, role))
				}
			}

			Expect(k8sClient.Get(ctx, typeNamespacedName, st)).To(Succeed())
			Expect(st.Status.Phase).To(Equal("Ready"))
			Expect(st.Status.CurrentSessionID).To(Equal("session-replay-1"))
			Expect(st.Status.KaiselPhase).To(Equal("Disabled"))
			Expect(st.Status.ReplayState).To(Equal(replayStateStarted))
			Expect(startedURLs).NotTo(BeEmpty())
			Expect(startedURLs[0]).To(ContainSubstring(":9090/v1/replay/start"))

			var gone enginev1alpha1.KaiselRule
			err := k8sClient.Get(ctx, kaiselRuleKey(st), &gone)
			Expect(errors.IsNotFound(err)).To(BeTrue())

			var deps appsv1.DeploymentList
			Expect(k8sClient.List(ctx, &deps, client.InNamespace(shadowNS))).To(Succeed())
			Expect(deps.Items).To(HaveLen(6))

			roles := map[string]struct{}{}
			for _, d := range deps.Items {
				if role := d.Labels[labelRole]; role != "" {
					roles[role] = struct{}{}
				}
			}
			Expect(roles).To(HaveKey(roleControlA))
			Expect(roles).To(HaveKey(roleControlB))
			Expect(roles).To(HaveKey(roleCandidate))

			var igrisDeploy appsv1.Deployment
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Namespace: shadowNS,
				Name:      igrisDeploymentName(st),
			}, &igrisDeploy)).To(Succeed())
			Expect(igrisDeploy.Spec.Template.Spec.Containers[0].Env).To(ContainElement(corev1.EnvVar{
				Name:  envOperatingMode,
				Value: modeReplay,
			}))
		})
	})
})

func int32Ptr(v int32) *int32 {
	return &v
}
