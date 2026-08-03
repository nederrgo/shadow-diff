package controller

import (
	"context"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"

	enginev1alpha1 "github.com/shadow-diff/monarch/api/v1alpha1"
)

const (
	localBeruName     = "beru-local"
	localBeruGRPCPort = int32(50051)
	localBeruHTTPPort = int32(8080)
	// localBeruIngestPort fronts the same HTTP server as localBeruHTTPPort.
	// It exists because the shadow pod's iptables init container REDIRECTs every
	// outbound connection to port 8080 into Envoy's egress listener, which would
	// answer a sidecar's report with "502 egress: no mock found". Reporting on a
	// port the redirect does not match is a three-line fix where an iptables
	// exemption would be a per-process one.
	localBeruIngestPort = int32(8081)
	// Disk EmptyDir for Bbolt WAL + dead-letter file (1 GiB matches Beru's WAL cap).
	localBeruWALSizeGi = int64(1)
)

func localBeruLabels(st *enginev1alpha1.ShadowTest) map[string]string {
	return map[string]string{
		labelManagedBy:           valueManagedBy,
		labelShadowTestName:      st.Name,
		labelShadowTestCRNS:      st.Namespace,
		labelShadowTestUID:       string(st.UID),
		"app":                    localBeruName,
		"app.kubernetes.io/name": localBeruName,
	}
}

func localBeruDNSHost(shadowNS string) string {
	return shadowServiceHost(shadowNS, localBeruName)
}

func (r *ShadowTestReconciler) reconcileLocalBeru(
	ctx context.Context,
	st *enginev1alpha1.ShadowTest,
	shadowNS string,
) error {
	podLabels := localBeruLabels(st)

	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: shadowNS,
			Name:      localBeruName,
		},
	}
	if _, err := ctrl.CreateOrPatch(ctx, r.Client, svc, func() error {
		svc.Labels = podLabels
		svc.Spec.Selector = map[string]string{"app": localBeruName}
		svc.Spec.Ports = []corev1.ServicePort{
			{Name: "grpc", Port: localBeruGRPCPort, TargetPort: intstr.FromInt32(localBeruGRPCPort), Protocol: corev1.ProtocolTCP},
			{Name: "ingest", Port: localBeruIngestPort, TargetPort: intstr.FromInt32(localBeruHTTPPort), Protocol: corev1.ProtocolTCP},
			{Name: "http", Port: localBeruHTTPPort, TargetPort: intstr.FromInt32(localBeruHTTPPort), Protocol: corev1.ProtocolTCP},
		}
		return nil
	}); err != nil {
		return err
	}

	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: shadowNS,
			Name:      localBeruName,
		},
	}
	replicas := int32(1)
	_, err := ctrl.CreateOrPatch(ctx, r.Client, deploy, func() error {
		deploy.Labels = podLabels
		deploy.Spec.Replicas = &replicas
		deploy.Spec.Selector = &metav1.LabelSelector{MatchLabels: map[string]string{"app": localBeruName}}
		deploy.Spec.Template.ObjectMeta.Labels = map[string]string{"app": localBeruName}
		for k, v := range podLabels {
			deploy.Spec.Template.ObjectMeta.Labels[k] = v
		}
		container, volumes := localBeruPodSpec(st)
		deploy.Spec.Template.Spec.Containers = []corev1.Container{container}
		deploy.Spec.Template.Spec.Volumes = volumes
		return nil
	})
	return err
}

// localBeruPodSpec builds the beru-local container and its volumes. Split out of
// the CreateOrPatch mutation so the storage-mode branch is testable without a
// cluster.
//
// Always mounts a disk EmptyDir at /data for the Bbolt WAL and dead-letter file.
// With BERU_DB_SECRET configured, DB_* arrives wholesale from the replicated
// Secret via envFrom — adding a connection setting later needs no controller change.
func localBeruPodSpec(st *enginev1alpha1.ShadowTest) (corev1.Container, []corev1.Volume) {
	_, dbSecretName, usesPostgres := beruDBSecretRef()

	container := corev1.Container{
		Name:            "beru",
		Image:           beruImageFor(st),
		ImagePullPolicy: corev1.PullIfNotPresent,
		Ports: []corev1.ContainerPort{
			{Name: "grpc", ContainerPort: localBeruGRPCPort, Protocol: corev1.ProtocolTCP},
			{Name: "http", ContainerPort: localBeruHTTPPort, Protocol: corev1.ProtocolTCP},
		},
		Env: []corev1.EnvVar{
			{Name: "BERU_GRPC_ADDR", Value: ":50051"},
			{Name: "BERU_HTTP_ADDR", Value: ":8080"},
			{Name: "BERU_SHADOW_TEST_NAME", Value: st.Name},
			// Session identity for durable storage: the same session folder
			// as the S3 layout, so diff rows join to recorded artifacts.
			{Name: envSessionID, Value: st.Status.CurrentSessionID},
			// Replay run identity: scopes Postgres diffs so re-playing the same
			// S3 session does not inflate occurrence counts. Empty in record.
			{Name: envReplayExecutionID, Value: st.Status.CurrentReplayExecutionID},
			{Name: "SHADOW_NAMESPACE", Value: st.Namespace},
			{Name: "SHADOW_MODE", Value: st.Spec.Mode},
		},
		VolumeMounts: []corev1.VolumeMount{{
			Name:      volumeNameLocalBeruData,
			MountPath: "/data",
		}},
		Resources: corev1.ResourceRequirements{
			Limits: corev1.ResourceList{
				corev1.ResourceMemory: resource.MustParse("128Mi"),
				corev1.ResourceCPU:    resource.MustParse("200m"),
			},
		},
	}

	if usesPostgres {
		container.EnvFrom = []corev1.EnvFromSource{{
			SecretRef: &corev1.SecretEnvSource{
				LocalObjectReference: corev1.LocalObjectReference{Name: dbSecretName},
			},
		}}
	}

	volumes := []corev1.Volume{{
		Name: volumeNameLocalBeruData,
		VolumeSource: corev1.VolumeSource{
			EmptyDir: &corev1.EmptyDirVolumeSource{
				SizeLimit: resource.NewQuantity(localBeruWALSizeGi<<30, resource.BinarySI),
			},
		},
	}}
	return container, volumes
}

func (r *ShadowTestReconciler) localBeruReady(
	ctx context.Context,
	shadowNS string,
) (bool, workloadWaitReason, error) {
	return r.deploymentBootReady(ctx, shadowNS, localBeruName, localBeruName)
}

// ponytail: used only in tests to assert label selector wiring
func localBeruPodSelector() labels.Selector {
	sel, _ := metav1.LabelSelectorAsSelector(&metav1.LabelSelector{
		MatchLabels: map[string]string{"app": localBeruName},
	})
	return sel
}
