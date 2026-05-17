package controller

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"

	enginev1alpha1 "github.com/shadow-diff/monarch/api/v1alpha1"
)

func envoyConfigMapName(st *enginev1alpha1.ShadowTest, role string) string {
	return sanitizeForDNS(fmt.Sprintf("%s-%s-envoy", st.Name, role))
}

func renderEnvoyYAML(st *enginev1alpha1.ShadowTest, role string) (string, error) {
	beruAddr := beruGRPCAddressFor(st)
	beruHost, beruPort, err := parseBeruHostPort(beruAddr)
	if err != nil {
		return "", fmt.Errorf("invalid beruGRPCAddress %q: %w", beruAddr, err)
	}
	appPort := applicationPortFor(st)
	ingressPort := st.Spec.ServicePort
	beruTimeout := beruGRPCTimeoutFor(st)

	return fmt.Sprintf(envoyYAMLTemplate,
		ingressPort,
		role,
		beruTimeout,
		role,
		appPort,
		beruHost,
		beruPort,
	), nil
}

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
  - name: egress_stub
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
`

func (r *ShadowTestReconciler) reconcileEnvoyConfigMap(
	ctx context.Context,
	st *enginev1alpha1.ShadowTest,
	shadowNS, role string,
) error {
	cmName := envoyConfigMapName(st, role)
	podLabels := deploymentPodLabels(st, role)

	yaml, err := renderEnvoyYAML(st, role)
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
