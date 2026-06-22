package controller

import (
	"encoding/json"
	"net"
	"sort"
	"strconv"

	corev1 "k8s.io/api/core/v1"

	enginev1alpha1 "github.com/shadow-diff/monarch/api/v1alpha1"
)

const (
	netObservContainerName = "netobserv-ebpf-agent"
	defaultNetObservImage  = "quay.io/netobserv/netobserv-ebpf-agent:main"
	netObservPCAPPort      = 9990
	netObservTargetHost    = "127.0.0.1"
	siphonPCAPListenAddr   = "127.0.0.1:9990"

	envEnablePCA          = "ENABLE_PCA"
	envTargetHost         = "TARGET_HOST"
	envTargetPort         = "TARGET_PORT"
	envFlowFilterRules    = "FLOW_FILTER_RULES"
	envSiphonPCAPAddr     = "SIPHON_PCAP_ADDR"
	envSampling           = "SAMPLING"
	envCacheActiveTimeout = "CACHE_ACTIVE_TIMEOUT"
	envCacheMaxFlows      = "CACHE_MAX_FLOWS"
	envDeduper            = "DEDUPER"
	envExcludeInterfaces  = "EXCLUDE_INTERFACES"
	envTCAttachMode       = "TC_ATTACH_MODE"
	envTCAttachRetries    = "TC_ATTACH_RETRIES"

	netObservNetnsVolumeName = "netns"
	siphonCoordVolumeName    = "siphon-coord"
	siphonGRPCReadyPath      = "/var/run/siphon/grpc-ready"
	netObservBinaryPath      = "/netobserv-ebpf-agent"

	// ponytail: stable DS env avoids rolling restarts on prod IP churn; Siphon filters via /v1/config
	netObservFlowFilterFallback = `[{"ip_cidr":"0.0.0.0/0","protocol":"TCP","action":"Accept"}]`
)

type flowFilterRule struct {
	IPCidr   string `json:"ip_cidr"`
	Protocol string `json:"protocol,omitempty"`
	Action   string `json:"action"`
}

// buildNetObservFlowFilterRules returns a JSON array for FLOW_FILTER_RULES.
// Uses json.Marshal only — never fmt.Sprintf("%q", ...) which double-escapes.
func buildNetObservFlowFilterRules(prodIPs []string) (string, error) {
	if len(prodIPs) == 0 {
		return netObservFlowFilterFallback, nil
	}
	seen := make(map[string]struct{}, len(prodIPs))
	rules := make([]flowFilterRule, 0, len(prodIPs))
	for _, ipStr := range prodIPs {
		ip := net.ParseIP(ipStr)
		if ip == nil || ip.To4() == nil {
			continue
		}
		cidr := ip.String() + "/32"
		if _, ok := seen[cidr]; ok {
			continue
		}
		seen[cidr] = struct{}{}
		rules = append(rules, flowFilterRule{
			IPCidr:   cidr,
			Protocol: "TCP",
			Action:   "Accept",
		})
	}
	if len(rules) == 0 {
		return netObservFlowFilterFallback, nil
	}
	sort.Slice(rules, func(i, j int) bool {
		return rules[i].IPCidr < rules[j].IPCidr
	})
	b, err := json.Marshal(rules)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func netObservStableFlowFilterRules() string {
	return netObservFlowFilterFallback
}

func netObservContainer(image string) corev1.Container {
	privileged := true
	runAsUser := int64(0)
	return corev1.Container{
		Name:            netObservContainerName,
		Image:           image,
		ImagePullPolicy: corev1.PullIfNotPresent,
		Command:         []string{"/bin/sh", "-c"},
		Args: []string{
			`i=0; while [ $i -lt 120 ]; do [ -f ` + siphonGRPCReadyPath + ` ] && break; i=$((i+1)); sleep 1; done; exec ` + netObservBinaryPath,
		},
		SecurityContext: &corev1.SecurityContext{
			Privileged: &privileged,
			RunAsUser:  &runAsUser,
		},
		Env: []corev1.EnvVar{
			{Name: envEnablePCA, Value: "true"},
			{Name: envCacheActiveTimeout, Value: "5s"},
			{Name: envCacheMaxFlows, Value: "50000"},
			{Name: envDeduper, Value: "firstCome"},
			{Name: envExcludeInterfaces, Value: ""},
			{Name: envTCAttachMode, Value: "tcx"},
			{Name: envTCAttachRetries, Value: "10"},
			{Name: envTargetHost, Value: netObservTargetHost},
			{Name: envTargetPort, Value: strconv.Itoa(netObservPCAPPort)},
			{Name: envFlowFilterRules, Value: netObservStableFlowFilterRules()},
		},
		VolumeMounts: []corev1.VolumeMount{
			{Name: netObservNetnsVolumeName, MountPath: "/var/run/netns", ReadOnly: true},
			{Name: siphonCoordVolumeName, MountPath: "/var/run/siphon"},
		},
	}
}

func netObservPodVolumes() []corev1.Volume {
	hostDir := corev1.HostPathDirectory
	return []corev1.Volume{{
		Name: netObservNetnsVolumeName,
		VolumeSource: corev1.VolumeSource{
			HostPath: &corev1.HostPathVolumeSource{Path: "/var/run/netns", Type: &hostDir},
		},
	}, {
		Name: siphonCoordVolumeName,
		VolumeSource: corev1.VolumeSource{
			EmptyDir: &corev1.EmptyDirVolumeSource{},
		},
	}}
}

func netObservImageFor(st *enginev1alpha1.ShadowTest) string {
	return resolveHelperImage(defaultNetObservImage, "", envNetObservImage)
}

func defaultNetObservImageResolved() string {
	return resolveHelperImage(defaultNetObservImage, "", envNetObservImage)
}
