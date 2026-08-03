package controller

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	enginev1alpha1 "github.com/shadow-diff/monarch/api/v1alpha1"
)

const replayStartTimeout = 10 * time.Second
const replayStateStarted = "started"

// replayAdminURL is the ingress hub admin POST /v1/replay/start (port 9090).
func replayAdminURL(st *enginev1alpha1.ShadowTest, shadowNS string) string {
	svc := igrisServiceName(st)
	if needsAMQPIngress(st) {
		svc = igrisRabbitMQServiceName(st)
	}
	host := shadowServiceHost(shadowNS, svc)
	return fmt.Sprintf("http://%s:%d/v1/replay/start", host, igrisAdminPort)
}

func replayWorkloadNames(st *enginev1alpha1.ShadowTest) []string {
	names := []string{shopServiceName()}
	if needsAMQPIngress(st) {
		names = append(names, igrisRabbitMQDeploymentName(st))
	} else {
		names = append(names, igrisDeploymentName(st))
	}
	names = append(names,
		shadowDeploymentName(st, roleControlA),
		shadowDeploymentName(st, roleControlB),
		shadowDeploymentName(st, roleCandidate),
	)
	return names
}

// deploymentsRollReady reports ReadyReplicas >= desired and UpdatedReplicas == Replicas.
func (r *ShadowTestReconciler) deploymentsRollReady(
	ctx context.Context,
	shadowNS string,
	names []string,
) (bool, error) {
	for _, name := range names {
		var deploy appsv1.Deployment
		if err := r.Get(ctx, client.ObjectKey{Namespace: shadowNS, Name: name}, &deploy); err != nil {
			return false, err
		}
		replicas := int32(1)
		if deploy.Spec.Replicas != nil {
			replicas = *deploy.Spec.Replicas
		}
		if replicas < 1 {
			replicas = 1
		}
		if deploy.Status.ReadyReplicas < replicas {
			return false, nil
		}
		if deploy.Status.UpdatedReplicas != deploy.Status.Replicas || deploy.Status.Replicas < replicas {
			return false, nil
		}
	}
	return true, nil
}

func defaultReplayStartPOST(ctx context.Context, url string) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	if err != nil {
		return 0, err
	}
	client := &http.Client{Timeout: replayStartTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode, nil
}

// maybeTriggerReplay POSTs Igris admin /v1/replay/start once when replay stack is roll-ready.
// Returns (requeue, err). requeue=true means caller should RequeueAfter without failing the CR.
func (r *ShadowTestReconciler) maybeTriggerReplay(
	ctx context.Context,
	st *enginev1alpha1.ShadowTest,
	shadowNS string,
) (requeue bool, err error) {
	if operatingMode(st) != modeReplay {
		return false, nil
	}
	if strings.TrimSpace(st.Status.ReplayState) != "" {
		return false, nil
	}

	ready, err := r.deploymentsRollReady(ctx, shadowNS, replayWorkloadNames(st))
	if err != nil {
		return true, err
	}
	if !ready {
		return true, nil
	}

	url := replayAdminURL(st, shadowNS)
	starter := r.ReplayStarter
	if starter == nil {
		starter = defaultReplayStartPOST
	}
	code, err := starter(ctx, url)
	if err != nil {
		return true, err
	}
	// 202 Accepted and 409 Conflict (already running) both mean replay is underway.
	if code < 200 || (code >= 300 && code != http.StatusConflict) {
		return true, fmt.Errorf("replay start returned HTTP %d", code)
	}

	base := st.DeepCopy()
	st.Status.ReplayState = replayStateStarted
	if err := r.Status().Patch(ctx, st, client.MergeFrom(base)); err != nil {
		return true, err
	}
	logf.FromContext(ctx).Info("Successfully triggered replay for session "+st.Status.CurrentSessionID,
		"sessionID", st.Status.CurrentSessionID,
		"url", url,
		"statusCode", code,
	)
	return false, nil
}
