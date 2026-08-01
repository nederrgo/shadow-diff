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
	defaultSamplePercentage = 100
)

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

func httpIngressCaptureEnabled(st *enginev1alpha1.ShadowTest, target *appsv1.Deployment) bool {
	if isAMQPOnlyShadowTest(st) {
		return false
	}
	targetPorts := targetPrimaryContainerPorts(target)
	appPort := applicationPortFor(st)
	svcPort := servicePortFor(st)
	for _, in := range resolvedInputs(st) {
		d := strings.TrimSpace(strings.ToLower(in.Driver))
		if d != "http_request" && d != "tcp_stream" {
			continue
		}
		if targetPorts[in.Port] || in.Port == appPort || in.Port == svcPort {
			return true
		}
	}
	return false
}

func samplePercentage(st *enginev1alpha1.ShadowTest) int {
	if st.Spec.SamplePercentage > 0 {
		return st.Spec.SamplePercentage
	}
	return defaultSamplePercentage
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

func kaiselIngressPorts(st *enginev1alpha1.ShadowTest) []int32 {
	if isAMQPOnlyShadowTest(st) {
		return nil
	}
	if app := applicationPortFor(st); app > 0 {
		return []int32{app}
	}
	var ports []int32
	seen := map[int32]bool{}
	for _, in := range resolvedInputs(st) {
		d := strings.TrimSpace(strings.ToLower(in.Driver))
		if d != "http_request" && d != "tcp_stream" {
			continue
		}
		if in.Port <= 0 || seen[in.Port] {
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

// int32ToUint16Ports converts application port numbers to uint16, skipping
// invalid values. Port 0 and values above 65535 are not valid TCP ports.
func int32ToUint16Ports(ports []int32) []uint16 {
	out := make([]uint16, 0, len(ports))
	for _, p := range ports {
		if p > 0 && p <= 65535 {
			out = append(out, uint16(p))
		}
	}
	return out
}

// ── KaiselRule (ingress capture) ───────────────────────────────────────────

func kaiselRuleName(st *enginev1alpha1.ShadowTest) string {
	return "kaisel-" + st.Name
}

func kaiselRuleKey(st *enginev1alpha1.ShadowTest) types.NamespacedName {
	return types.NamespacedName{Namespace: st.Namespace, Name: kaiselRuleName(st)}
}

// kaiselIgrisBaseURL is the ClusterIP Service URL Kaisel POSTs admitted
// ingress copies to for this ShadowTest.
func kaiselIgrisBaseURL(st *enginev1alpha1.ShadowTest) string {
	return shadowServiceURL(shadowNamespaceForCR(st), igrisServiceName(st), servicePortFor(st))
}

// kaiselEgressBaseURL is the shadow-namespace Shop Service URL Kaisel POSTs
// captured egress request/response pairs to. Shop derives the mock key that
// its Envoy ext_proc later looks up, so Kaisel only sends flat fields.
func kaiselEgressBaseURL(shadowNS string) string {
	return "http://" + shopHTTPHostFor(shadowNS)
}

// reconcileKaiselRule creates or updates a KaiselRule whose targetIPs are the
// live, Running pod IPs for the target Deployment. Only pods that are Running,
// have a non-empty PodIP, and have no DeletionTimestamp are included.
func (r *ShadowTestReconciler) reconcileKaiselRule(
	ctx context.Context,
	st *enginev1alpha1.ShadowTest,
	shadowNS string,
	target *appsv1.Deployment,
) error {
	var podList corev1.PodList
	if err := r.List(ctx, &podList,
		client.InNamespace(targetNamespaceFor(st)),
		client.MatchingLabels(target.Spec.Template.Labels),
	); err != nil {
		return fmt.Errorf("list target pods: %w", err)
	}

	var ips []string
	for _, pod := range podList.Items {
		if pod.DeletionTimestamp != nil {
			continue
		}
		if pod.Status.Phase != corev1.PodRunning {
			continue
		}
		if pod.Status.PodIP == "" {
			continue
		}
		ips = append(ips, pod.Status.PodIP)
	}
	sort.Strings(ips)

	ports := int32ToUint16Ports(kaiselIngressPorts(st))
	igrisURL := kaiselIgrisBaseURL(st)
	egressURL := kaiselEgressBaseURL(shadowNS)
	samplePct := samplePercentage(st)

	// Read the existing rule to skip the patch when nothing changed.
	var existing enginev1alpha1.KaiselRule
	existingErr := r.Get(ctx, kaiselRuleKey(st), &existing)
	if existingErr == nil &&
		ipSetsEqual(existing.Spec.TargetIPs, ips) &&
		portSetsEqual(existing.Spec.TargetPorts, ports) &&
		existing.Spec.IgrisBaseURL == igrisURL &&
		existing.Spec.EgressBaseURL == egressURL &&
		existing.Spec.SamplePercentage == samplePct {
		// No change; avoid a spurious write.
		return nil
	}

	rule := &enginev1alpha1.KaiselRule{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: st.Namespace,
			Name:      kaiselRuleName(st),
		},
	}
	_, err := ctrl.CreateOrPatch(ctx, r.Client, rule, func() error {
		rule.Labels = map[string]string{
			labelManagedBy:      valueManagedBy,
			labelShadowTestName: st.Name,
		}
		rule.Spec = enginev1alpha1.KaiselRuleSpec{
			TargetIPs:        ips,
			TargetPorts:      ports,
			IgrisBaseURL:     igrisURL,
			EgressBaseURL:    egressURL,
			SamplePercentage: samplePct,
		}
		return controllerutil.SetControllerReference(st, rule, r.Scheme)
	})
	if err != nil {
		return err
	}

	if err := r.Get(ctx, kaiselRuleKey(st), rule); err != nil {
		return err
	}
	if rule.Status.Phase == "Active" {
		return nil
	}
	base := rule.DeepCopy()
	rule.Status.Phase = "Active"
	return r.Status().Patch(ctx, rule, client.MergeFrom(base))
}

func (r *ShadowTestReconciler) deleteKaiselRule(ctx context.Context, st *enginev1alpha1.ShadowTest) error {
	rule := &enginev1alpha1.KaiselRule{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: st.Namespace,
			Name:      kaiselRuleName(st),
		},
	}
	if err := r.Delete(ctx, rule); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	return nil
}

// ipSetsEqual reports whether two sorted IP lists are identical.
func ipSetsEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// portSetsEqual reports whether two port lists are identical (order independent).
func portSetsEqual(a, b []uint16) bool {
	if len(a) != len(b) {
		return false
	}
	m := make(map[uint16]bool, len(a))
	for _, p := range a {
		m[p] = true
	}
	for _, p := range b {
		if !m[p] {
			return false
		}
	}
	return true
}

// ── reconcileKaiselCapture (KaiselRule: ingress + egress) ──────────────────

func (r *ShadowTestReconciler) reconcileKaiselCapture(
	ctx context.Context,
	st *enginev1alpha1.ShadowTest,
	shadowNS string,
	target *appsv1.Deployment,
) (captureTargets []string, phase string, err error) {
	labels := copyStringMap(target.Spec.Template.Labels)

	if err := r.reconcileKaiselRule(ctx, st, shadowNS, target); err != nil {
		return formatCaptureTargets(labels), capturePhaseDegraded, err
	}
	return formatCaptureTargets(labels), capturePhaseReady, nil
}

// ── Deployment→ShadowTest watch mapper ────────────────────────────────────

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
