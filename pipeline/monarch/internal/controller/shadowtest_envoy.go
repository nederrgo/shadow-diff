package controller

import (
	"context"
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

func renderEnvoyYAML(st *enginev1alpha1.ShadowTest, shadowNS, role string) (string, error) {
	beruAddr := beruGRPCAddressFor(st, shadowNS)
	beruHost, beruPort, err := parseBeruHostPort(beruAddr)
	if err != nil {
		return "", fmt.Errorf("invalid beruGRPCAddress %q: %w", beruAddr, err)
	}
	appPort := applicationPortFor(st)
	ingressPort := servicePortFor(st)
	beruTimeout := beruGRPCTimeoutFor(st)

	egressListener := buildEgressHTTPListenerYAML(role, beruTimeout)

	shopAddr := shopGRPCAddressFor(shadowNS)
	shopHost, shopPort, err := parseHostPort(shopAddr)
	if err != nil {
		return "", fmt.Errorf("invalid shopGRPCAddress %q: %w", shopAddr, err)
	}

	extraListeners := egressListener
	extraClusters := buildShopExtProcClusterYAML(shopHost, shopPort)

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

func buildShopExtProcClusterYAML(host string, port int32) string {
	var b strings.Builder
	b.WriteString("  - name: shop_ext_proc\n")
	b.WriteString("    type: STRICT_DNS\n")
	b.WriteString("    connect_timeout: 5s\n")
	b.WriteString("    typed_extension_protocol_options:\n")
	b.WriteString("      envoy.extensions.upstreams.http.v3.HttpProtocolOptions:\n")
	b.WriteString("        \"@type\": type.googleapis.com/envoy.extensions.upstreams.http.v3.HttpProtocolOptions\n")
	b.WriteString("        explicit_http_config:\n")
	b.WriteString("          http2_protocol_options: {}\n")
	b.WriteString("    load_assignment:\n")
	b.WriteString("      cluster_name: shop_ext_proc\n")
	b.WriteString("      endpoints:\n")
	b.WriteString("      - lb_endpoints:\n")
	b.WriteString("        - endpoint:\n")
	b.WriteString("            address:\n")
	b.WriteString("              socket_address:\n")
	fmt.Fprintf(&b, "                address: %s\n", host)
	fmt.Fprintf(&b, "                port_value: %d\n", port)
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

func buildEgressHTTPListenerYAML(role, beruTimeout string) string {
	var b strings.Builder
	b.WriteString("  - name: egress_http_listener\n")
	b.WriteString("    address:\n")
	b.WriteString("      socket_address:\n")
	b.WriteString("        address: 0.0.0.0\n")
	fmt.Fprintf(&b, "        port_value: %d\n", egressProxyPort)
	b.WriteString("    filter_chains:\n")
	b.WriteString("    - filters:\n")
	b.WriteString("      - name: envoy.filters.network.http_connection_manager\n")
	b.WriteString("        typed_config:\n")
	b.WriteString("          \"@type\": type.googleapis.com/envoy.extensions.filters.network.http_connection_manager.v3.HttpConnectionManager\n")
	b.WriteString("          stat_prefix: egress_http\n")
	b.WriteString("          route_config:\n")
	b.WriteString("            name: outbound_routes\n")
	b.WriteString("            virtual_hosts:\n")
	b.WriteString("            - name: egress_passthrough\n")
	b.WriteString("              domains: [\"*\"]\n")
	b.WriteString("              routes:\n")
	b.WriteString("              - match:\n")
	b.WriteString("                  prefix: \"/\"\n")
	b.WriteString("                direct_response:\n")
	b.WriteString("                  status: 502\n")
	b.WriteString("                  body:\n")
	b.WriteString("                    inline_string: \"egress: no mock found\"\n")
	b.WriteString("          http_filters:\n")
	appendEgressExtProcFilterYAML(&b, role, beruTimeout)
	b.WriteString("          - name: envoy.filters.http.router\n")
	b.WriteString("            typed_config:\n")
	b.WriteString("              \"@type\": type.googleapis.com/envoy.extensions.filters.http.router.v3.Router\n")
	return b.String()
}

func appendEgressExtProcFilterYAML(b *strings.Builder, role, beruTimeout string) {
	b.WriteString("          - name: envoy.filters.http.ext_proc\n")
	b.WriteString("            typed_config:\n")
	b.WriteString("              \"@type\": type.googleapis.com/envoy.extensions.filters.http.ext_proc.v3.ExternalProcessor\n")
	b.WriteString("              grpc_service:\n")
	b.WriteString("                envoy_grpc:\n")
	b.WriteString("                  cluster_name: shop_ext_proc\n")
	fmt.Fprintf(b, "                timeout: %s\n", beruTimeout)
	b.WriteString("                initial_metadata:\n")
	b.WriteString("                - key: x-shadow-mode\n")
	b.WriteString("                  value: \"egress\"\n")
	b.WriteString("                - key: x-shadow-role\n")
	fmt.Fprintf(b, "                  value: %q\n", role)
	b.WriteString("              failure_mode_allow: false\n")
	b.WriteString("              processing_mode:\n")
	b.WriteString("                request_header_mode: SEND\n")
	b.WriteString("                request_body_mode: BUFFERED\n")
	b.WriteString("                response_header_mode: SKIP\n")
	b.WriteString("                response_body_mode: NONE\n")
}

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
  - name: beru_ext_proc
    type: STRICT_DNS
    connect_timeout: 5s
    typed_extension_protocol_options:
      envoy.extensions.upstreams.http.v3.HttpProtocolOptions:
        "@type": type.googleapis.com/envoy.extensions.upstreams.http.v3.HttpProtocolOptions
        explicit_http_config:
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
