package controller

import (
	"context"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	otelv1alpha1 "github.com/open-telemetry/opentelemetry-operator/apis/v1alpha1"
	enginev1alpha1 "github.com/shadow-diff/monarch/api/v1alpha1"
)

const otelInstrumentationName = "shadow-diff-telemetry"

// +kubebuilder:rbac:groups=opentelemetry.io,resources=instrumentations,verbs=get;list;watch;create;update;patch;delete

func (r *ShadowTestReconciler) reconcileOTelInstrumentation(
	ctx context.Context,
	st *enginev1alpha1.ShadowTest,
	shadowNS string,
) error {
	if !otelInjectionEnabled(st) {
		existing := &otelv1alpha1.Instrumentation{
			ObjectMeta: metav1.ObjectMeta{Name: otelInstrumentationName, Namespace: shadowNS},
		}
		return client.IgnoreNotFound(r.Delete(ctx, existing))
	}

	otelCR := &otelv1alpha1.Instrumentation{
		ObjectMeta: metav1.ObjectMeta{Name: otelInstrumentationName, Namespace: shadowNS},
	}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, otelCR, func() error {
		if otelCR.Labels == nil {
			otelCR.Labels = map[string]string{}
		}
		for k, v := range map[string]string{
			labelManagedBy:      valueManagedBy,
			labelShadowTestName: st.Name,
			labelShadowTestCRNS: st.Namespace,
			labelShadowTestUID:  string(st.UID),
		} {
			otelCR.Labels[k] = v
		}
		otelCR.Spec.Exporter.Endpoint = beruOTLPEndpointFor(st, shadowNS)
		otelCR.Spec.Propagators = []otelv1alpha1.Propagator{otelv1alpha1.TraceContext}
		otelCR.Spec.Sampler.Type = otelv1alpha1.ParentBasedTraceIDRatio
		otelCR.Spec.Sampler.Argument = "1"
		// ponytail: no SetControllerReference — Instrumentation lives in shadow-ns, ShadowTest in CR ns;
		// namespace delete on ShadowTest teardown GCs the CR.
		return nil
	})
	return err
}
