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

func renderEnvoyYAML(st *enginev1alpha1.ShadowTest, shadowNS, role string) (string, error) {
	beruAddr := beruGRPCAddressFor(st, shadowNS)
	beruHost, beruPort, err := parseBeruHostPort(beruAddr)
	if err != nil {
		return "", fmt.Errorf("invalid beruGRPCAddress %q: %w", beruAddr, err)
	}
	appPort := applicationPortFor(st)
	ingressPort := servicePortFor(st)
	beruTimeout := beruGRPCTimeoutFor(st)

	egressListener, err := buildEgressHTTPListenerYAML(st, role, beruTimeout)
	if err != nil {
		return "", err
	}

	ingestHost, ingestPort, err := parseBeruIngestHostPort(st, shadowNS)
	if err != nil {
		return "", fmt.Errorf("invalid beruIngestAddress: %w", err)
	}

	extraListeners := egressListener
	extraClusters := buildDynamicForwardProxyClusterYAML()

	return fmt.Sprintf(envoyYAMLTemplate,
		ingressPort,
		role,
		beruTimeout,
		role,
		extraListeners,
		appPort,
		beruHost,
		beruPort,
		ingestHost,
		ingestPort,
		extraClusters,
	), nil
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

func recordAndReplayConfigJSON(st *enginev1alpha1.ShadowTest) (string, error) {
	type entry struct {
		Host               string   `json:"host"`
		IgnoreRequestPaths []string `json:"ignoreRequestPaths,omitempty"`
	}
	entries := make([]entry, 0, len(st.Spec.RecordAndReplay))
	for _, d := range st.Spec.RecordAndReplay {
		host, _, ignorePaths := recordAndReplayEntry(d)
		entries = append(entries, entry{
			Host:               host,
			IgnoreRequestPaths: ignorePaths,
		})
	}
	raw, err := json.Marshal(entries)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

const (
	dynamicForwardProxyCluster  = "dynamic_egress_cluster"
	dynamicForwardProxyDNSCache = "dynamic_forward_proxy_cache"

	envoyLuaEgressScript = `local max_bytes = 65536

local function escape_json(s)
  if s == nil then return "" end
  s = tostring(s)
  s = s:gsub("\\", "\\\\"):gsub('"', '\\"'):gsub("\n", "\\n"):gsub("\r", "\\r")
  return s
end

local function read_body(handle, truncated_suffix)
  local body = handle:body()
  if body == nil then return "" end
  local len = body:length()
  if len == 0 then return "" end
  local n = len
  if n > max_bytes then n = max_bytes end
  local chunk = body:getBytes(0, n)
  if len > max_bytes then
    return chunk .. truncated_suffix
  end
  return chunk
end

function envoy_on_request(request_handle)
  local traceparent = request_handle:headers():get("traceparent")
  if traceparent then
    request_handle:streamInfo():dynamicMetadata():set("envoy.filters.http.lua", "traceparent", traceparent)
  end
  local method = request_handle:headers():get(":method") or ""
  local path = request_handle:headers():get(":path") or "/"
  request_handle:streamInfo():dynamicMetadata():set("envoy.filters.http.lua", "http_method", method)
  request_handle:streamInfo():dynamicMetadata():set("envoy.filters.http.lua", "http_path", path)
  local req_body = read_body(request_handle, "\n[TRUNCATED BY ENVOY PROXY]")
  request_handle:streamInfo():dynamicMetadata():set("envoy.filters.http.lua", "req_body", req_body)
end

function envoy_on_response(response_handle)
  local meta = response_handle:streamInfo():dynamicMetadata():get("envoy.filters.http.lua")
  local traceparent = ""
  local method = ""
  local path = "/"
  local req_body = ""
  if meta then
    traceparent = meta["traceparent"] or ""
    method = meta["http_method"] or ""
    path = meta["http_path"] or "/"
    req_body = meta["req_body"] or ""
  end
  local resp_body = read_body(response_handle, "\n[TRUNCATED BY ENVOY PROXY]")
  local metadata_json = string.format('{"method":"%s","path":"%s"}', escape_json(method), escape_json(path))
  local json_payload = string.format(
    '{"trace_id":"%s","pod_role":"%s","shadow_test_name":"%s","protocol":"http","direction":"egress","raw_request":"%s","raw_response":"%s","metadata":"%s"}',
    escape_json(traceparent),
    escape_json(os.getenv("SHADOW_ROLE") or ""),
    escape_json(os.getenv("SHADOW_TEST_NAME") or ""),
    escape_json(req_body),
    escape_json(resp_body),
    escape_json(metadata_json)
  )
  local headers, body = response_handle:httpCall(
    "POST",
    "beru_ingest",
    "/api/v1/ingest/wire",
    json_payload,
    5000)
  if headers == nil then
    response_handle:logInfo("beru_ingest httpCall failed")
  end
end
`
)

func buildEgressHTTPListenerYAML(st *enginev1alpha1.ShadowTest, role, beruTimeout string) (string, error) {
	recordAndReplayJSON := "[]"
	if len(st.Spec.RecordAndReplay) > 0 {
		raw, err := recordAndReplayConfigJSON(st)
		if err != nil {
			return "", err
		}
		recordAndReplayJSON = raw
	}
	domains := recordAndReplayEgressDomains(st)
	return renderEgressHTTPListenerYAML(role, beruTimeout, domains, recordAndReplayJSON, len(st.Spec.RecordAndReplay) > 0), nil
}

func renderEgressHTTPListenerYAML(role, beruTimeout string, recordDomains []string, recordAndReplayJSON string, hasRecordAndReplay bool) string {
	var b strings.Builder
	b.WriteString("  - name: egress_http_listener\n")
	b.WriteString("    address:\n")
	b.WriteString("      socket_address:\n")
	b.WriteString("        address: 127.0.0.1\n")
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
	if hasRecordAndReplay && len(recordDomains) > 0 {
		b.WriteString("            - name: egress_record_and_replay\n")
		b.WriteString("              domains:\n")
		for _, d := range recordDomains {
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
	} else {
		b.WriteString("            - name: external_apis\n")
		b.WriteString("              domains: [\"*\"]\n")
		b.WriteString("              routes:\n")
		b.WriteString("              - match:\n")
		b.WriteString("                  connect_matcher: {}\n")
		b.WriteString("                route:\n")
		fmt.Fprintf(&b, "                  cluster: %s\n", dynamicForwardProxyCluster)
		b.WriteString("              - match:\n")
		b.WriteString("                  prefix: \"/\"\n")
		b.WriteString("                route:\n")
		fmt.Fprintf(&b, "                  cluster: %s\n", dynamicForwardProxyCluster)
	}
	b.WriteString("          http_filters:\n")
	appendEgressLuaFilterYAML(&b)
	appendEgressExtProcFilterYAML(&b, role, beruTimeout, recordAndReplayJSON, hasRecordAndReplay)
	if !hasRecordAndReplay {
		b.WriteString("          - name: envoy.filters.http.dynamic_forward_proxy\n")
		b.WriteString("            typed_config:\n")
		b.WriteString("              \"@type\": type.googleapis.com/envoy.extensions.filters.http.dynamic_forward_proxy.v3.FilterConfig\n")
		b.WriteString("              dns_cache_config:\n")
		fmt.Fprintf(&b, "                name: %s\n", dynamicForwardProxyDNSCache)
		b.WriteString("                dns_lookup_family: V4_ONLY\n")
	}
	b.WriteString("          - name: envoy.filters.http.router\n")
	b.WriteString("            typed_config:\n")
	b.WriteString("              \"@type\": type.googleapis.com/envoy.extensions.filters.http.router.v3.Router\n")
	return b.String()
}

func appendEgressLuaFilterYAML(b *strings.Builder) {
	b.WriteString("          - name: envoy.filters.http.lua\n")
	b.WriteString("            typed_config:\n")
	b.WriteString("              \"@type\": type.googleapis.com/envoy.extensions.filters.http.lua.v3.Lua\n")
	b.WriteString("              inline_code: |\n")
	for _, line := range strings.Split(strings.TrimSuffix(envoyLuaEgressScript, "\n"), "\n") {
		fmt.Fprintf(b, "                %s\n", line)
	}
}

func appendEgressExtProcFilterYAML(b *strings.Builder, role, beruTimeout, recordAndReplayJSON string, hasRecordAndReplay bool) {
	b.WriteString("          - name: envoy.filters.http.ext_proc\n")
	b.WriteString("            typed_config:\n")
	b.WriteString("              \"@type\": type.googleapis.com/envoy.extensions.filters.http.ext_proc.v3.ExternalProcessor\n")
	b.WriteString("              grpc_service:\n")
	b.WriteString("                envoy_grpc:\n")
	b.WriteString("                  cluster_name: beru_ext_proc\n")
	fmt.Fprintf(b, "                timeout: %s\n", beruTimeout)
	b.WriteString("                initial_metadata:\n")
	b.WriteString("                - key: x-shadow-mode\n")
	b.WriteString("                  value: \"egress\"\n")
	b.WriteString("                - key: x-shadow-role\n")
	fmt.Fprintf(b, "                  value: %q\n", role)
	if hasRecordAndReplay {
		b.WriteString("                - key: x-shadow-record-and-replay-config\n")
		fmt.Fprintf(b, "                  value: %q\n", recordAndReplayJSON)
		b.WriteString("              failure_mode_allow: false\n")
		b.WriteString("              processing_mode:\n")
		b.WriteString("                request_header_mode: SEND\n")
		b.WriteString("                request_body_mode: BUFFERED\n")
		b.WriteString("                response_header_mode: SKIP\n")
		b.WriteString("                response_body_mode: NONE\n")
	} else {
		b.WriteString("              failure_mode_allow: true\n")
		b.WriteString("              processing_mode:\n")
		b.WriteString("                request_header_mode: SEND\n")
		b.WriteString("                response_header_mode: SKIP\n")
		b.WriteString("                request_body_mode: NONE\n")
		b.WriteString("                response_body_mode: NONE\n")
	}
}

func buildDynamicForwardProxyClusterYAML() string {
	var b strings.Builder
	fmt.Fprintf(&b, "  - name: %s\n", dynamicForwardProxyCluster)
	b.WriteString("    lb_policy: CLUSTER_PROVIDED\n")
	b.WriteString("    cluster_type:\n")
	b.WriteString("      name: envoy.clusters.dynamic_forward_proxy\n")
	b.WriteString("      typed_config:\n")
	b.WriteString("        \"@type\": type.googleapis.com/envoy.extensions.clusters.dynamic_forward_proxy.v3.ClusterConfig\n")
	b.WriteString("        dns_cache_config:\n")
	fmt.Fprintf(&b, "          name: %s\n", dynamicForwardProxyDNSCache)
	b.WriteString("          dns_lookup_family: V4_ONLY\n")
	b.WriteString("    transport_socket:\n")
	b.WriteString("      name: envoy.transport_sockets.tls\n")
	b.WriteString("      typed_config:\n")
	b.WriteString("        \"@type\": type.googleapis.com/envoy.extensions.transport_sockets.tls.v3.UpstreamTlsContext\n")
	return b.String()
}

func renderEgressListenerYAML(st *enginev1alpha1.ShadowTest, role, beruTimeout string) (string, error) {
	return buildEgressHTTPListenerYAML(st, role, beruTimeout)
}

func buildEgressProxyListenerYAML(role, beruTimeout string, domains []string, recordAndReplayJSON string) string {
	return renderEgressHTTPListenerYAML(role, beruTimeout, domains, recordAndReplayJSON, true)
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
  - name: beru_ingest
    type: STRICT_DNS
    connect_timeout: 5s
    load_assignment:
      cluster_name: beru_ingest
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
