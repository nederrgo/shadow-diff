package controller

import (
	"context"
	"fmt"
	"sort"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	enginev1alpha1 "github.com/shadow-diff/monarch/api/v1alpha1"
)

const (
	defaultSiphonMaxPayloadSize = 65536
	shadowSiphonServiceName     = "siphon"
	shadowSiphonOTLPPort        = 4317
)

func pixieCaptureEnabled(st *enginev1alpha1.ShadowTest, target *appsv1.Deployment) bool {
	return siphonEnabled(st, target) || egressRecordingEnabled(st)
}

func targetPrimaryContainerPorts(target *appsv1.Deployment) map[int32]bool {
	ports := map[int32]bool{}
	if target == nil || len(target.Spec.Template.Spec.Containers) == 0 {
		return ports
	}
	for _, p := range target.Spec.Template.Spec.Containers[0].Ports {
		ports[p.ContainerPort] = true
	}
	return ports
}

func siphonIngressCaptureEnabled(st *enginev1alpha1.ShadowTest, target *appsv1.Deployment) bool {
	if isAMQPOnlyShadowTest(st) {
		return false
	}
	targetPorts := targetPrimaryContainerPorts(target)
	if len(targetPorts) == 0 {
		return false
	}
	for _, in := range resolvedInputs(st) {
		d := strings.TrimSpace(strings.ToLower(in.Driver))
		if d != "http_request" && d != "tcp_stream" {
			continue
		}
		if targetPorts[in.Port] {
			return true
		}
	}
	return false
}

func siphonEnabled(st *enginev1alpha1.ShadowTest, target *appsv1.Deployment) bool {
	if st.Spec.Siphon != nil && st.Spec.Siphon.Enabled != nil && !*st.Spec.Siphon.Enabled {
		return false
	}
	if st.Spec.Siphon != nil && st.Spec.Siphon.Enabled != nil && *st.Spec.Siphon.Enabled {
		return true
	}
	if siphonIngressCaptureEnabled(st, target) {
		return true
	}
	return false
}

func siphonMaxPayloadSize(st *enginev1alpha1.ShadowTest) int64 {
	if st.Spec.Siphon != nil && st.Spec.Siphon.MaxPayloadSize > 0 {
		return st.Spec.Siphon.MaxPayloadSize
	}
	return defaultSiphonMaxPayloadSize
}

func siphonExcludePaths(st *enginev1alpha1.ShadowTest) []string {
	if st.Spec.Siphon == nil || len(st.Spec.Siphon.ExcludePaths) == 0 {
		return nil
	}
	out := make([]string, len(st.Spec.Siphon.ExcludePaths))
	copy(out, st.Spec.Siphon.ExcludePaths)
	return out
}

func formatCaptureTargets(labels map[string]string) []string {
	if len(labels) == 0 {
		return nil
	}
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, len(keys))
	for i, k := range keys {
		out[i] = k + "=" + labels[k]
	}
	return out
}

func shadowSiphonOTelEndpoint(shadowNS string) string {
	return fmt.Sprintf("%s.%s.svc.cluster.local:%d", shadowSiphonServiceName, shadowNS, shadowSiphonOTLPPort)
}

func shadowRecorderOTelEndpoint(st *enginev1alpha1.ShadowTest, shadowNS string) string {
	return fmt.Sprintf("%s.%s.svc.cluster.local:%d", recorderServiceName(st), shadowNS, recorderOTLPPort)
}

func pixieStreamRuleName(st *enginev1alpha1.ShadowTest) string {
	return "pixie-" + st.Name
}

func pixieStreamRuleKey(st *enginev1alpha1.ShadowTest) types.NamespacedName {
	return types.NamespacedName{Namespace: st.Namespace, Name: pixieStreamRuleName(st)}
}

func siphonIngressPorts(st *enginev1alpha1.ShadowTest) []int32 {
	if isAMQPOnlyShadowTest(st) {
		return nil
	}
	var ports []int32
	seen := map[int32]bool{}
	for _, in := range resolvedInputs(st) {
		if seen[in.Port] {
			continue
		}
		seen[in.Port] = true
		ports = append(ports, in.Port)
	}
	return ports
}

func copyStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func buildPixieStreamRuleSpec(
	st *enginev1alpha1.ShadowTest,
	shadowNS string,
	target *appsv1.Deployment,
) enginev1alpha1.PixieStreamRuleSpec {
	ingress := siphonEnabled(st, target)
	egress := egressRecordingEnabled(st)

	spec := enginev1alpha1.PixieStreamRuleSpec{
		ShadowTestRef:   st.Namespace + "/" + st.Name,
		Active:          true,
		TargetNamespace: targetNamespaceFor(st),
		TargetLabels:    copyStringMap(target.Spec.Template.Labels),
		MaxPayloadSize:  siphonMaxPayloadSize(st),
		ExcludePaths:    siphonExcludePaths(st),
	}
	if ingress {
		spec.OTelEndpoint = shadowSiphonOTelEndpoint(shadowNS)
		spec.TargetPorts = siphonIngressPorts(st)
	}
	if egress {
		spec.RecorderOTelEndpoint = shadowRecorderOTelEndpoint(st, shadowNS)
		for _, h := range st.Spec.RecordAndReplay {
			host, _, _ := recordAndReplayEntry(h)
			if host != "" {
				spec.RecordAndReplayHosts = append(spec.RecordAndReplayHosts, host)
			}
		}
	}
	return spec
}

func (r *ShadowTestReconciler) ensureShadowSiphonService(ctx context.Context, shadowNS string) error {
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: shadowNS,
			Name:      shadowSiphonServiceName,
			Labels: map[string]string{
				labelManagedBy:           valueManagedBy,
				"app.kubernetes.io/name": shadowSiphonServiceName,
			},
		},
	}
	_, err := ctrl.CreateOrPatch(ctx, r.Client, svc, func() error {
		svc.Labels = map[string]string{
			labelManagedBy:           valueManagedBy,
			"app.kubernetes.io/name": shadowSiphonServiceName,
		}
		svc.Spec.Type = corev1.ServiceTypeClusterIP
		svc.Spec.Selector = map[string]string{
			"app.kubernetes.io/name": shadowSiphonServiceName,
		}
		svc.Spec.Ports = []corev1.ServicePort{{
			Name:     "otlp-grpc",
			Port:     shadowSiphonOTLPPort,
			Protocol: corev1.ProtocolTCP,
		}}
		return nil
	})
	return err
}

func (r *ShadowTestReconciler) reconcilePixieStreamRule(
	ctx context.Context,
	st *enginev1alpha1.ShadowTest,
	shadowNS string,
	target *appsv1.Deployment,
) error {
	rule := &enginev1alpha1.PixieStreamRule{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: st.Namespace,
			Name:      pixieStreamRuleName(st),
			Labels: map[string]string{
				labelManagedBy:      valueManagedBy,
				labelShadowTestName: st.Name,
			},
		},
	}
	spec := buildPixieStreamRuleSpec(st, shadowNS, target)
	_, err := ctrl.CreateOrPatch(ctx, r.Client, rule, func() error {
		rule.Labels = map[string]string{
			labelManagedBy:      valueManagedBy,
			labelShadowTestName: st.Name,
		}
		rule.Spec = spec
		if err := controllerutil.SetControllerReference(st, rule, r.Scheme); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return err
	}

	if err := r.Get(ctx, pixieStreamRuleKey(st), rule); err != nil {
		return err
	}
	rule.Status.Phase = "Active"
	return r.Status().Update(ctx, rule)
}

func (r *ShadowTestReconciler) deactivatePixieStreamRule(ctx context.Context, st *enginev1alpha1.ShadowTest) error {
	key := pixieStreamRuleKey(st)
	var rule enginev1alpha1.PixieStreamRule
	if err := r.Get(ctx, key, &rule); err != nil {
		return client.IgnoreNotFound(err)
	}
	base := rule.DeepCopy()
	rule.Spec.Active = false
	if err := r.Patch(ctx, &rule, client.MergeFrom(base)); err != nil {
		return client.IgnoreNotFound(err)
	}
	if err := r.Get(ctx, key, &rule); err != nil {
		return client.IgnoreNotFound(err)
	}
	rule.Status.Phase = "Inactive"
	return r.Status().Update(ctx, &rule)
}

func (r *ShadowTestReconciler) deletePixieStreamRule(ctx context.Context, st *enginev1alpha1.ShadowTest) error {
	rule := &enginev1alpha1.PixieStreamRule{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: st.Namespace,
			Name:      pixieStreamRuleName(st),
		},
	}
	if err := r.Delete(ctx, rule); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	return nil
}

func (r *ShadowTestReconciler) reconcileSiphonCapture(
	ctx context.Context,
	st *enginev1alpha1.ShadowTest,
	shadowNS string,
	target *appsv1.Deployment,
) (captureTargets []string, siphonPhase string, err error) {
	labels := copyStringMap(target.Spec.Template.Labels)
	ingress := siphonEnabled(st, target)

	if !pixieCaptureEnabled(st, target) {
		if err := r.deletePixieStreamRule(ctx, st); err != nil {
			return formatCaptureTargets(labels), "Degraded", err
		}
		return nil, "Disabled", nil
	}

	if ingress {
		if err := r.ensureShadowSiphonService(ctx, shadowNS); err != nil {
			return formatCaptureTargets(labels), "Degraded", err
		}
	}
	if err := r.reconcilePixieStreamRule(ctx, st, shadowNS, target); err != nil {
		return formatCaptureTargets(labels), "Degraded", err
	}
	return formatCaptureTargets(labels), "Ready", nil
}

func (r *ShadowTestReconciler) mapDeploymentToShadowTests(ctx context.Context, obj client.Object) []reconcile.Request {
	dep, ok := obj.(*appsv1.Deployment)
	if !ok {
		return nil
	}
	var list enginev1alpha1.ShadowTestList
	if err := r.List(ctx, &list); err != nil {
		return nil
	}
	var out []reconcile.Request
	for _, st := range list.Items {
		if targetNamespaceFor(&st) == dep.Namespace && st.Spec.TargetDeployment == dep.Name {
			out = append(out, reconcile.Request{
				NamespacedName: types.NamespacedName{Namespace: st.Namespace, Name: st.Name},
			})
		}
	}
	return out
}
