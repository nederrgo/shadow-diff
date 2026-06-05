package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"

	enginev1alpha1 "github.com/shadow-diff/monarch/api/v1alpha1"
)

func envoyConfigMapName(st *enginev1alpha1.ShadowTest, role string) string {
	return sanitizeForDNS(fmt.Sprintf("%s-%s-envoy", st.Name, role))
}

func hasMongoDependency(st *enginev1alpha1.ShadowTest) bool {
	for _, dep := range st.Spec.Dependencies {
		if dep.Port == mongoProxyPort {
			return true
		}
	}
	return false
}

func mongoDependency(st *enginev1alpha1.ShadowTest) (enginev1alpha1.DependencySpec, bool) {
	for _, dep := range st.Spec.Dependencies {
		if dep.Port == mongoProxyPort {
			return dep, true
		}
	}
	return enginev1alpha1.DependencySpec{}, false
}

func renderEnvoyYAML(st *enginev1alpha1.ShadowTest, shadowNS, role string) (string, error) {
	beruAddr := beruGRPCAddressFor(st)
	beruHost, beruPort, err := parseBeruHostPort(beruAddr)
	if err != nil {
		return "", fmt.Errorf("invalid beruGRPCAddress %q: %w", beruAddr, err)
	}
	appPort := applicationPortFor(st)
	ingressPort := st.Spec.ServicePort
	beruTimeout := beruGRPCTimeoutFor(st)

	egressListener, err := renderEgressListenerYAML(st, role, beruTimeout)
	if err != nil {
		return "", err
	}

	extraListeners := egressListener
	extraClusters := ""
	if hasMongoDependency(st) {
		dep, ok := mongoDependency(st)
		if !ok {
			return "", fmt.Errorf("mongo dependency expected but not found")
		}
		extraListeners += buildMongoEgressListenerYAML(role, beruTimeout)
		extraClusters = buildMongoEgressClustersYAML(shadowNS, dep, role, beruHost, beruPort)
	}

	return fmt.Sprintf(envoyYAMLTemplate,
		ingressPort,
		role,
		beruTimeout,
		role,
		extraListeners,
		appPort,
		beruHost,
		beruPort,
		extraClusters,
	), nil
}

func buildMongoEgressListenerYAML(role, beruTimeout string) string {
	var b strings.Builder
	b.WriteString("  - name: mongo_egress\n")
	b.WriteString("    address:\n")
	b.WriteString("      socket_address:\n")
	b.WriteString("        address: 127.0.0.1\n")
	fmt.Fprintf(&b, "        port_value: %d\n", mongoProxyPort)
	b.WriteString("    filter_chains:\n")
	b.WriteString("    - filters:\n")
	b.WriteString("      - name: envoy.filters.network.mongo_proxy\n")
	b.WriteString("        typed_config:\n")
	b.WriteString("          \"@type\": type.googleapis.com/envoy.extensions.filters.network.mongo_proxy.v3.MongoProxy\n")
	b.WriteString("          stat_prefix: mongo_egress\n")
	b.WriteString("          emit_dynamic_metadata: true\n")
	b.WriteString("      - name: envoy.filters.network.tcp_proxy\n")
	b.WriteString("        typed_config:\n")
	b.WriteString("          \"@type\": type.googleapis.com/envoy.extensions.filters.network.tcp_proxy.v3.TcpProxy\n")
	b.WriteString("          stat_prefix: mongo_egress\n")
	fmt.Fprintf(&b, "          cluster: %s\n", mongoUpstreamCluster)
	b.WriteString("          access_log:\n")
	b.WriteString("          - name: envoy.access_loggers.tcp_grpc\n")
	b.WriteString("            typed_config:\n")
	b.WriteString("              \"@type\": type.googleapis.com/envoy.extensions.access_loggers.grpc.v3.TcpGrpcAccessLogConfig\n")
	b.WriteString("              common_config:\n")
	b.WriteString("                log_name: mongo_egress_")
	fmt.Fprintf(&b, "%s\n", role)
	b.WriteString("                transport_api_version: V3\n")
	b.WriteString("                grpc_service:\n")
	b.WriteString("                  envoy_grpc:\n")
	fmt.Fprintf(&b, "                    cluster_name: %s\n", clusterBeruALS)
	fmt.Fprintf(&b, "                  timeout: %s\n", beruTimeout)
	b.WriteString("                custom_tags:\n")
	b.WriteString("                - tag: x-shadow-role\n")
	b.WriteString("                  literal:\n")
	b.WriteString("                    value: ")
	fmt.Fprintf(&b, "%q\n", role)
	return b.String()
}

func buildMongoEgressClustersYAML(shadowNS string, dep enginev1alpha1.DependencySpec, role, beruHost string, beruPort int32) string {
	upstreamHost := dependencyEndpoint(shadowNS, dep.Name, role, dep.Port)
	var host string
	var port int32 = dep.Port
	if h, p, err := parseHostPort(upstreamHost); err == nil {
		host, port = h, p
	} else {
		host = upstreamHost
	}
	var b strings.Builder
	fmt.Fprintf(&b, "  - name: %s\n", mongoUpstreamCluster)
	b.WriteString("    type: STRICT_DNS\n")
	b.WriteString("    connect_timeout: 5s\n")
	fmt.Fprintf(&b, "    load_assignment:\n      cluster_name: %s\n", mongoUpstreamCluster)
	b.WriteString("      endpoints:\n      - lb_endpoints:\n        - endpoint:\n            address:\n              socket_address:\n")
	fmt.Fprintf(&b, "                address: %s\n                port_value: %d\n", host, port)
	fmt.Fprintf(&b, "  - name: %s\n", clusterBeruALS)
	b.WriteString("    type: STRICT_DNS\n")
	b.WriteString("    connect_timeout: 5s\n")
	b.WriteString("    http2_protocol_options: {}\n")
	fmt.Fprintf(&b, "    load_assignment:\n      cluster_name: %s\n", clusterBeruALS)
	b.WriteString("      endpoints:\n      - lb_endpoints:\n        - endpoint:\n            address:\n              socket_address:\n")
	fmt.Fprintf(&b, "                address: %s\n                port_value: %d\n", beruHost, beruPort)
	return b.String()
}

func parseHostPort(endpoint string) (host string, port int32, err error) {
	h, p, err := net.SplitHostPort(endpoint)
	if err != nil {
		return "", 0, err
	}
	var portNum int
	if _, err := fmt.Sscanf(p, "%d", &portNum); err != nil {
		return "", 0, err
	}
	return h, int32(portNum), nil
}

// egressVirtualHostDomains returns Envoy virtual_host domains for downstream hosts.
// Each host is listed bare and with :* so :authority values with explicit ports match.
func egressVirtualHostDomains(hosts []string) []string {
	seen := make(map[string]struct{}, len(hosts)*2)
	var domains []string
	for _, host := range hosts {
		host = strings.TrimSpace(host)
		if host == "" {
			continue
		}
		for _, d := range []string{host, host + ":*"} {
			if _, ok := seen[d]; ok {
				continue
			}
			seen[d] = struct{}{}
			domains = append(domains, d)
		}
	}
	return domains
}

func downstreamsConfigJSON(st *enginev1alpha1.ShadowTest) (string, error) {
	type entry struct {
		Host               string   `json:"host"`
		IgnoreRequestPaths []string `json:"ignoreRequestPaths,omitempty"`
	}
	entries := make([]entry, 0, len(st.Spec.Downstreams))
	for _, d := range st.Spec.Downstreams {
		entries = append(entries, entry{
			Host:               d.Host,
			IgnoreRequestPaths: d.IgnoreRequestPaths,
		})
	}
	raw, err := json.Marshal(entries)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func renderEgressListenerYAML(st *enginev1alpha1.ShadowTest, role, beruTimeout string) (string, error) {
	if len(st.Spec.Downstreams) == 0 {
		return egressStubListenerYAML, nil
	}

	hosts := make([]string, 0, len(st.Spec.Downstreams))
	for _, d := range st.Spec.Downstreams {
		hosts = append(hosts, d.Host)
	}
	domains := egressVirtualHostDomains(hosts)
	if len(domains) == 0 {
		return egressStubListenerYAML, nil
	}

	downstreamsJSON, err := downstreamsConfigJSON(st)
	if err != nil {
		return "", err
	}

	return buildEgressProxyListenerYAML(role, beruTimeout, domains, downstreamsJSON), nil
}

func buildEgressProxyListenerYAML(role, beruTimeout string, domains []string, downstreamsJSON string) string {
	var b strings.Builder
	b.WriteString("  - name: egress_proxy\n")
	b.WriteString("    address:\n")
	b.WriteString("      socket_address:\n")
	b.WriteString("        address: 127.0.0.1\n")
	fmt.Fprintf(&b, "        port_value: %d\n", egressProxyPort)
	b.WriteString("    filter_chains:\n")
	b.WriteString("    - filters:\n")
	b.WriteString("      - name: envoy.filters.network.http_connection_manager\n")
	b.WriteString("        typed_config:\n")
	b.WriteString("          \"@type\": type.googleapis.com/envoy.extensions.filters.network.http_connection_manager.v3.HttpConnectionManager\n")
	b.WriteString("          stat_prefix: egress_proxy\n")
	b.WriteString("          route_config:\n")
	b.WriteString("            name: egress_routes\n")
	b.WriteString("            virtual_hosts:\n")
	b.WriteString("            - name: egress_downstreams\n")
	b.WriteString("              domains:\n")
	for _, d := range domains {
		fmt.Fprintf(&b, "              - %q\n", d)
	}
	b.WriteString("              routes:\n")
	b.WriteString("              - match:\n")
	b.WriteString("                  prefix: \"/\"\n")
	b.WriteString("                route:\n")
	b.WriteString("                  cluster: egress_blackhole\n")
	b.WriteString("            - name: egress_reject\n")
	b.WriteString("              domains: [\"*\"]\n")
	b.WriteString("              routes:\n")
	b.WriteString("              - match:\n")
	b.WriteString("                  prefix: \"/\"\n")
	b.WriteString("                direct_response:\n")
	b.WriteString("                  status: 403\n")
	b.WriteString("                  body:\n")
	b.WriteString("                    inline_string: \"egress host not configured\"\n")
	b.WriteString("          # traceparent pass-through on egress (OTel agent outbound HTTP).\n")
	b.WriteString("          http_filters:\n")
	b.WriteString("          - name: envoy.filters.http.ext_proc\n")
	b.WriteString("            typed_config:\n")
	b.WriteString("              \"@type\": type.googleapis.com/envoy.extensions.filters.http.ext_proc.v3.ExternalProcessor\n")
	b.WriteString("              grpc_service:\n")
	b.WriteString("                envoy_grpc:\n")
	b.WriteString("                  cluster_name: beru_ext_proc\n")
	fmt.Fprintf(&b, "                timeout: %s\n", beruTimeout)
	b.WriteString("                initial_metadata:\n")
	b.WriteString("                - key: x-shadow-mode\n")
	b.WriteString("                  value: \"egress\"\n")
	b.WriteString("                - key: x-shadow-role\n")
	fmt.Fprintf(&b, "                  value: %q\n", role)
	b.WriteString("                - key: x-shadow-downstreams-config\n")
	fmt.Fprintf(&b, "                  value: %q\n", downstreamsJSON)
	b.WriteString("              failure_mode_allow: false\n")
	b.WriteString("              processing_mode:\n")
	b.WriteString("                request_header_mode: SEND\n")
	b.WriteString("                request_body_mode: BUFFERED\n")
	b.WriteString("                response_header_mode: SKIP\n")
	b.WriteString("                response_body_mode: NONE\n")
	b.WriteString("          - name: envoy.filters.http.router\n")
	b.WriteString("            typed_config:\n")
	b.WriteString("              \"@type\": type.googleapis.com/envoy.extensions.filters.http.router.v3.Router\n")
	return b.String()
}

const egressStubListenerYAML = `  - name: egress_stub
    address:
      socket_address:
        address: 127.0.0.1
        port_value: 15001
    filter_chains:
    - filters:
      - name: envoy.filters.network.http_connection_manager
        typed_config:
          "@type": type.googleapis.com/envoy.extensions.filters.network.http_connection_manager.v3.HttpConnectionManager
          stat_prefix: egress_stub
          route_config:
            name: egress_blackhole
            virtual_hosts:
            - name: blackhole
              domains: ["*"]
              routes:
              - match:
                  prefix: "/"
                direct_response:
                  status: 503
                  body:
                    inline_string: "egress not implemented"
          http_filters:
          - name: envoy.filters.http.router
            typed_config:
              "@type": type.googleapis.com/envoy.extensions.filters.http.router.v3.Router
`

// Ingress and egress HCM forward traceparent by default (no header removal on traceparent).
// Igris synthesizes traceparent on multicast; Envoy preserves it through ingress and egress ext_proc.
const envoyYAMLTemplate = `admin:
  address:
    socket_address:
      address: 0.0.0.0
      port_value: 9901
static_resources:
  listeners:
  - name: ingress
    address:
      socket_address:
        address: 0.0.0.0
        port_value: %d
    filter_chains:
    - filters:
      - name: envoy.filters.network.http_connection_manager
        typed_config:
          "@type": type.googleapis.com/envoy.extensions.filters.network.http_connection_manager.v3.HttpConnectionManager
          stat_prefix: ingress
          generate_request_id: true
          route_config:
            name: local_route
            virtual_hosts:
            - name: local_service
              domains: ["*"]
              routes:
              - match:
                  prefix: "/"
                route:
                  cluster: local_app
          # traceparent is not mutated here; pass-through for W3C context from Igris/OTel agent.
          http_filters:
          - name: envoy.filters.http.header_mutation
            typed_config:
              "@type": type.googleapis.com/envoy.extensions.filters.http.header_mutation.v3.HeaderMutation
              mutations:
                request_mutations:
                - append:
                    header:
                      key: x-shadow-trace-id
                      value: "%%REQ(x-request-id)%%"
                    append_action: ADD_IF_ABSENT
                - append:
                    header:
                      key: x-shadow-role
                      value: "%s"
          - name: envoy.filters.http.ext_proc
            typed_config:
              "@type": type.googleapis.com/envoy.extensions.filters.http.ext_proc.v3.ExternalProcessor
              grpc_service:
                envoy_grpc:
                  cluster_name: beru_ext_proc
                timeout: %s
                initial_metadata:
                - key: x-shadow-role
                  value: "%s"
              failure_mode_allow: true
              processing_mode:
                request_header_mode: SEND
                response_header_mode: SEND
                request_body_mode: NONE
                response_body_mode: BUFFERED
          - name: envoy.filters.http.router
            typed_config:
              "@type": type.googleapis.com/envoy.extensions.filters.http.router.v3.Router
%s
  clusters:
  - name: local_app
    type: STATIC
    connect_timeout: 5s
    load_assignment:
      cluster_name: local_app
      endpoints:
      - lb_endpoints:
        - endpoint:
            address:
              socket_address:
                address: 127.0.0.1
                port_value: %d
  - name: egress_blackhole
    type: STATIC
    connect_timeout: 1s
    load_assignment:
      cluster_name: egress_blackhole
      endpoints:
      - lb_endpoints:
        - endpoint:
            address:
              socket_address:
                address: 127.0.0.1
                port_value: 1
  - name: beru_ext_proc
    type: STRICT_DNS
    connect_timeout: 5s
    http2_protocol_options: {}
    load_assignment:
      cluster_name: beru_ext_proc
      endpoints:
      - lb_endpoints:
        - endpoint:
            address:
              socket_address:
                address: %s
                port_value: %d
%s
`

func (r *ShadowTestReconciler) reconcileEnvoyConfigMap(
	ctx context.Context,
	st *enginev1alpha1.ShadowTest,
	shadowNS, role string,
) error {
	cmName := envoyConfigMapName(st, role)
	podLabels := deploymentPodLabels(st, role)

	yaml, err := renderEnvoyYAML(st, shadowNS, role)
	if err != nil {
		return err
	}

	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: shadowNS,
			Name:      cmName,
		},
	}

	_, err = ctrl.CreateOrPatch(ctx, r.Client, cm, func() error {
		if cm.Labels == nil {
			cm.Labels = map[string]string{}
		}
		for k, v := range podLabels {
			cm.Labels[k] = v
		}
		if cm.Data == nil {
			cm.Data = map[string]string{}
		}
		cm.Data[configMapKeyEnvoyYAML] = yaml
		return nil
	})
	return err
}
