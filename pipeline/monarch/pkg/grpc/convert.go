package grpc

import (
	enginev1alpha1 "github.com/shadow-diff/monarch/api/v1alpha1"
	"github.com/shadow-diff/monarchpb"
)

// ToStatusUpdate projects a ShadowTest onto the wire contract.
//
// Every enum mapper falls back to its UNSPECIFIED member rather than erroring: a
// CR written by an older Monarch, or one carrying a value this build does not know,
// must still stream rather than break the subscriber's whole feed.
func ToStatusUpdate(st *enginev1alpha1.ShadowTest) *monarchpb.ShadowTestStatusUpdate {
	if st == nil {
		return nil
	}
	c := st.Status.Components
	return &monarchpb.ShadowTestStatusUpdate{
		TestName:         st.Name,
		Namespace:        st.Namespace,
		Phase:            toPhase(st.Status.Phase),
		BootStep:         toBootStep(st.Status.BootStep),
		CurrentSessionId: st.Status.CurrentSessionID,
		ReplayState:      st.Status.ReplayState,
		Mode:             toMode(st.OperatingMode()),
		Message:          st.Status.Message,
		KaiselPhase:      toCapturePhase(st.Status.KaiselPhase),
		Components: &monarchpb.ComponentStatus{
			IgrisReady:       c.IgrisReady,
			ShopReady:        c.ShopReady,
			BeruReady:        c.BeruReady,
			KaiselRuleActive: c.KaiselRuleActive,
			AmqpBound:        c.AMQPBound,
			ShadowRolesReady: c.ShadowRolesReady,
			TargetDeployment: c.TargetDeployment,
		},
	}
}

func toPhase(s string) monarchpb.Phase {
	switch s {
	case enginev1alpha1.PhaseProgressing:
		return monarchpb.Phase_PHASE_PROGRESSING
	case enginev1alpha1.PhaseReady:
		return monarchpb.Phase_PHASE_READY
	case enginev1alpha1.PhaseFailed:
		return monarchpb.Phase_PHASE_FAILED
	case enginev1alpha1.PhaseDeleting:
		return monarchpb.Phase_PHASE_DELETING
	case enginev1alpha1.PhaseDeleted:
		return monarchpb.Phase_PHASE_DELETED
	default:
		return monarchpb.Phase_PHASE_UNSPECIFIED
	}
}

func toBootStep(s enginev1alpha1.BootStep) monarchpb.BootStep {
	switch s {
	case enginev1alpha1.BootStepValidating:
		return monarchpb.BootStep_BOOT_STEP_VALIDATING_INPUTS
	case enginev1alpha1.BootStepProvisioningSinks:
		return monarchpb.BootStep_BOOT_STEP_PROVISIONING_SINKS
	case enginev1alpha1.BootStepActivatingEgressTap:
		return monarchpb.BootStep_BOOT_STEP_ACTIVATING_EGRESS_TAP
	case enginev1alpha1.BootStepBindingAMQP:
		return monarchpb.BootStep_BOOT_STEP_BINDING_AMQP
	case enginev1alpha1.BootStepProvisioningShadow:
		return monarchpb.BootStep_BOOT_STEP_PROVISIONING_SHADOW
	case enginev1alpha1.BootStepReady:
		return monarchpb.BootStep_BOOT_STEP_READY
	case enginev1alpha1.BootStepFailed:
		return monarchpb.BootStep_BOOT_STEP_FAILED
	default:
		return monarchpb.BootStep_BOOT_STEP_UNSPECIFIED
	}
}

func toCapturePhase(s string) monarchpb.CapturePhase {
	switch s {
	case enginev1alpha1.CapturePhaseReady:
		return monarchpb.CapturePhase_CAPTURE_PHASE_READY
	case enginev1alpha1.CapturePhaseDegraded:
		return monarchpb.CapturePhase_CAPTURE_PHASE_DEGRADED
	case enginev1alpha1.CapturePhaseDisabled:
		return monarchpb.CapturePhase_CAPTURE_PHASE_DISABLED
	default:
		return monarchpb.CapturePhase_CAPTURE_PHASE_UNSPECIFIED
	}
}

func toMode(s string) monarchpb.Mode {
	switch s {
	case enginev1alpha1.ModeRecord:
		return monarchpb.Mode_MODE_RECORD
	case enginev1alpha1.ModeReplay:
		return monarchpb.Mode_MODE_REPLAY
	default:
		return monarchpb.Mode_MODE_UNSPECIFIED
	}
}
