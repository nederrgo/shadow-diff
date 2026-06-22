package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	enginev1alpha1 "github.com/shadow-diff/monarch/api/v1alpha1"
)

const (
	siphonSystemNamespace = "siphon-system"
	siphonDaemonSetName   = "siphon-agent"
	siphonAPIPort         = 8080
)

type siphonConfigPayload struct {
	SampleRate int            `json:"sample_rate"`
	Targets    []siphonTarget `json:"targets"`
}

type siphonRecordAndReplayHost struct {
	Host        string   `json:"host"`
	IgnorePaths []string `json:"ignore_paths,omitempty"`
}

type siphonTarget struct {
	ShadowTest      string                      `json:"shadowtest"`
	TargetIPs       []string                    `json:"target_ips"`
	TargetPorts     []int                       `json:"target_ports"`
	IgrisHost       string                      `json:"igris_host"`
	Listeners       []siphonListener            `json:"listeners"`
	RecorderHost    string                      `json:"recorder_host"`
	RecordAndReplay []siphonRecordAndReplayHost `json:"recordAndReplay,omitempty"`
	ExcludeIPs      []string                    `json:"exclude_ips,omitempty"`
}

type siphonListener struct {
	Port   int    `json:"port"`
	Driver string `json:"driver"`
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
	if len(st.Spec.RecordAndReplay) > 0 {
		return true
	}
	if siphonIngressCaptureEnabled(st, target) {
		return true
	}
	return false
}

func siphonSampleRate(st *enginev1alpha1.ShadowTest) int {
	if st.Spec.Siphon != nil && st.Spec.Siphon.SampleRate != nil {
		return int(*st.Spec.Siphon.SampleRate)
	}
	return 100
}

func (r *ShadowTestReconciler) ensureSiphonDaemonSet(ctx context.Context, siphonImage, netObservImage string) error {
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: siphonSystemNamespace}}
	_, err := ctrl.CreateOrPatch(ctx, r.Client, ns, func() error {
		if ns.Labels == nil {
			ns.Labels = map[string]string{"app.kubernetes.io/name": "siphon"}
		}
		return nil
	})
	if err != nil {
		return err
	}

	ds := &appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Namespace: siphonSystemNamespace, Name: siphonDaemonSetName}}
	_, err = ctrl.CreateOrPatch(ctx, r.Client, ds, func() error {
		if ds.Labels == nil {
			ds.Labels = map[string]string{"app.kubernetes.io/name": siphonDaemonSetName}
		}
		ds.Spec.Selector = &metav1.LabelSelector{MatchLabels: map[string]string{"app.kubernetes.io/name": siphonDaemonSetName}}
		maxUnavailable := intstr.FromInt(1)
		ds.Spec.UpdateStrategy = appsv1.DaemonSetUpdateStrategy{
			Type: appsv1.RollingUpdateDaemonSetStrategyType,
			RollingUpdate: &appsv1.RollingUpdateDaemonSet{
				MaxUnavailable: &maxUnavailable,
			},
		}
		ds.Spec.Template.ObjectMeta.Labels = map[string]string{"app.kubernetes.io/name": siphonDaemonSetName}
		ds.Spec.Template.Spec.HostNetwork = true
		ds.Spec.Template.Spec.DNSPolicy = corev1.DNSClusterFirstWithHostNet
		ds.Spec.Template.Spec.ServiceAccountName = siphonDaemonSetName
		runAsUser := int64(0)
		runAsGroup := int64(0)
		privEscalation := false
		siphonContainer := corev1.Container{
			Name:            "agent",
			Image:           siphonImage,
			ImagePullPolicy: corev1.PullIfNotPresent,
			SecurityContext: &corev1.SecurityContext{
				RunAsUser:                &runAsUser,
				RunAsGroup:               &runAsGroup,
				AllowPrivilegeEscalation: &privEscalation,
				Capabilities: &corev1.Capabilities{
					Drop: []corev1.Capability{"ALL"},
					Add:  []corev1.Capability{"NET_RAW", "NET_ADMIN"},
				},
			},
			Env: []corev1.EnvVar{
				{Name: envSiphonPCAPAddr, Value: siphonPCAPListenAddr},
				{Name: "SIPHON_API_ADDR", Value: ":8080"},
			},
			Ports: []corev1.ContainerPort{{Name: "http", ContainerPort: siphonAPIPort}},
			VolumeMounts: []corev1.VolumeMount{
				{Name: siphonCoordVolumeName, MountPath: "/var/run/siphon"},
			},
		}
		ds.Spec.Template.Spec.Containers = []corev1.Container{
			siphonContainer,
			netObservContainer(netObservImage),
		}
		ds.Spec.Template.Spec.Volumes = netObservPodVolumes()
		ds.Spec.Template.Spec.Tolerations = []corev1.Toleration{{Operator: corev1.TolerationOpExists}}
		return nil
	})
	return err
}

func (r *ShadowTestReconciler) listTargetPodIPs(ctx context.Context, dep *appsv1.Deployment) ([]string, error) {
	selector, err := metav1.LabelSelectorAsSelector(dep.Spec.Selector)
	if err != nil {
		return nil, err
	}
	var pods corev1.PodList
	if err := r.List(ctx, &pods, client.InNamespace(dep.Namespace), client.MatchingLabelsSelector{Selector: selector}); err != nil {
		return nil, err
	}
	var ips []string
	for _, p := range pods.Items {
		if p.Status.PodIP == "" || p.Status.Phase != corev1.PodRunning {
			continue
		}
		ips = append(ips, p.Status.PodIP)
	}
	return ips, nil
}

func buildSiphonTarget(st *enginev1alpha1.ShadowTest, shadowNS string, podIPs []string, excludeIPs []string) siphonTarget {
	var ports []int
	var listeners []siphonListener
	var host string

	if !isAMQPOnlyShadowTest(st) {
		inputs := resolvedInputs(st)
		seen := map[int32]bool{}
		for _, in := range inputs {
			if seen[in.Port] {
				continue
			}
			seen[in.Port] = true
			ports = append(ports, int(in.Port))
			listeners = append(listeners, siphonListener{Port: int(in.Port), Driver: in.Driver})
		}
		host = shadowServiceHost(shadowNS, igrisServiceName(st))
	}
	var recordAndReplay []siphonRecordAndReplayHost
	for _, d := range st.Spec.RecordAndReplay {
		recordAndReplay = append(recordAndReplay, siphonRecordAndReplayHost{
			Host:        d.Host,
			IgnorePaths: d.IgnoreRequestPaths,
		})
	}
	recorderHost := ""
	if egressRecordingEnabled(st) {
		recorderHost = recorderHostFor(st, shadowNS)
	}
	return siphonTarget{
		ShadowTest:      st.Namespace + "/" + st.Name,
		TargetIPs:       podIPs,
		TargetPorts:     ports,
		IgrisHost:       host,
		Listeners:       listeners,
		RecorderHost:    recorderHost,
		RecordAndReplay: recordAndReplay,
		ExcludeIPs:      excludeIPs,
	}
}

func (r *ShadowTestReconciler) resolveSiphonExcludeIPs(ctx context.Context, shadowNS string, st *enginev1alpha1.ShadowTest) []string {
	var ips []string
	var igrisSvc corev1.Service
	if err := r.Get(ctx, types.NamespacedName{Namespace: shadowNS, Name: igrisServiceName(st)}, &igrisSvc); err == nil {
		if ip := igrisSvc.Spec.ClusterIP; ip != "" && ip != "None" {
			ips = append(ips, ip)
		}
	}
	var beruSvc corev1.Service
	if err := r.Get(ctx, types.NamespacedName{Namespace: beruSystemNamespace, Name: beruServiceName}, &beruSvc); err == nil {
		if ip := beruSvc.Spec.ClusterIP; ip != "" && ip != "None" {
			ips = append(ips, ip)
		}
	}
	if egressRecordingEnabled(st) {
		var recorderSvc corev1.Service
		if err := r.Get(ctx, types.NamespacedName{Namespace: shadowNS, Name: recorderServiceName(st)}, &recorderSvc); err == nil {
			if ip := recorderSvc.Spec.ClusterIP; ip != "" && ip != "None" {
				ips = append(ips, ip)
			}
		}
	}
	return ips
}

func (r *ShadowTestReconciler) pushGlobalSiphonConfig(ctx context.Context, pending *siphonTarget) (siphonPhase string, err error) {
	var list enginev1alpha1.ShadowTestList
	if err := r.List(ctx, &list); err != nil {
		return "", err
	}

	payload := siphonConfigPayload{SampleRate: 100}
	var targets []siphonTarget
	seen := map[string]struct{}{}
	for i := range list.Items {
		st := &list.Items[i]
		if st.Status.Phase != "Ready" {
			continue
		}
		if st.Status.ShadowNamespace == "" {
			continue
		}
		var dep appsv1.Deployment
		key := types.NamespacedName{Namespace: st.Spec.TargetNamespace, Name: st.Spec.TargetDeployment}
		if err := r.Get(ctx, key, &dep); err != nil {
			continue
		}
		if !siphonEnabled(st, &dep) {
			continue
		}
		ips, err := r.listTargetPodIPs(ctx, &dep)
		if err != nil {
			return "Degraded", err
		}
		payload.SampleRate = siphonSampleRate(st)
		excludeIPs := r.resolveSiphonExcludeIPs(ctx, st.Status.ShadowNamespace, st)
		t := buildSiphonTarget(st, st.Status.ShadowNamespace, ips, excludeIPs)
		if egressRecordingEnabled(st) && t.RecorderHost == "" {
			logf.FromContext(ctx).Info("Siphon target missing recorder_host despite recordAndReplay",
				"shadowtest", t.ShadowTest)
		}
		targets = append(targets, t)
		seen[t.ShadowTest] = struct{}{}
	}
	if pending != nil {
		if _, ok := seen[pending.ShadowTest]; !ok {
			targets = append(targets, *pending)
		}
	}
	payload.Targets = targets

	siphonImg := defaultSiphonImage()
	netObservImg := defaultNetObservImageResolved()
	for i := range list.Items {
		if siphonEnabled(&list.Items[i], nil) && list.Items[i].Status.Phase == "Ready" {
			siphonImg = siphonImageFor(&list.Items[i])
			netObservImg = netObservImageFor(&list.Items[i])
			break
		}
	}
	if err := r.ensureSiphonDaemonSet(ctx, siphonImg, netObservImg); err != nil {
		return "Degraded", err
	}

	if len(payload.Targets) == 0 {
		return "Disabled", nil
	}

	if err := r.postSiphonConfigToAgents(ctx, payload); err != nil {
		return "Degraded", nil
	}
	return "Ready", nil
}

// siphonAgentAPIHost returns the address Monarch uses to reach the Siphon HTTP API.
// Siphon runs with hostNetwork; on Kind the API listens on the node hostIP, not podIP.
func siphonAgentAPIHost(pod corev1.Pod) string {
	if pod.Spec.HostNetwork && pod.Status.HostIP != "" {
		return pod.Status.HostIP
	}
	return pod.Status.PodIP
}

func (r *ShadowTestReconciler) postSiphonConfigToAgents(ctx context.Context, payload siphonConfigPayload) error {
	var pods corev1.PodList
	if err := r.List(ctx, &pods, client.InNamespace(siphonSystemNamespace), client.MatchingLabels{
		"app.kubernetes.io/name": siphonDaemonSetName,
	}); err != nil {
		return err
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 10 * time.Second}
	var firstErr error
	for _, pod := range pods.Items {
		apiHost := siphonAgentAPIHost(pod)
		if apiHost == "" || pod.Status.Phase != corev1.PodRunning {
			continue
		}
		url := fmt.Sprintf("http://%s:%d/v1/config", apiHost, siphonAPIPort)
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode >= 300 {
			if firstErr == nil {
				firstErr = fmt.Errorf("siphon agent %s returned %s", pod.Name, resp.Status)
			}
		}
	}
	return firstErr
}

func (r *ShadowTestReconciler) reconcileSiphonCapture(
	ctx context.Context,
	st *enginev1alpha1.ShadowTest,
	shadowNS string,
	target *appsv1.Deployment,
) (captureIPs []string, siphonPhase string, err error) {
	if !siphonEnabled(st, target) {
		return nil, "Disabled", nil
	}
	if err := r.ensureSiphonDaemonSet(ctx, siphonImageFor(st), netObservImageFor(st)); err != nil {
		return nil, "Degraded", err
	}
	ips, err := r.listTargetPodIPs(ctx, target)
	if err != nil {
		return nil, "Degraded", err
	}
	excludeIPs := r.resolveSiphonExcludeIPs(ctx, shadowNS, st)
	pending := buildSiphonTarget(st, shadowNS, ips, excludeIPs)
	phase, err := r.pushGlobalSiphonConfig(ctx, &pending)
	return ips, phase, err
}

// mapPodToShadowTests enqueues ShadowTests when a prod pod changes.
func (r *ShadowTestReconciler) mapPodToShadowTests(ctx context.Context, obj client.Object) []reconcile.Request {
	pod, ok := obj.(*corev1.Pod)
	if !ok {
		return nil
	}
	var list enginev1alpha1.ShadowTestList
	if err := r.List(ctx, &list); err != nil {
		return nil
	}
	var out []reconcile.Request
	for _, st := range list.Items {
		if st.Spec.TargetNamespace != pod.Namespace {
			continue
		}
		var dep appsv1.Deployment
		key := types.NamespacedName{Namespace: st.Spec.TargetNamespace, Name: st.Spec.TargetDeployment}
		if err := r.Get(ctx, key, &dep); err != nil {
			continue
		}
		selector, err := metav1.LabelSelectorAsSelector(dep.Spec.Selector)
		if err != nil {
			continue
		}
		if selector.Matches(labels.Set(pod.Labels)) {
			out = append(out, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: st.Namespace, Name: st.Name}})
		}
	}
	return out
}
