package grpc

import (
	"testing"

	enginev1alpha1 "github.com/shadow-diff/monarch/api/v1alpha1"
	"github.com/shadow-diff/monarchpb"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestToStatusUpdate_FullyPopulated(t *testing.T) {
	st := &enginev1alpha1.ShadowTest{
		ObjectMeta: metav1.ObjectMeta{Name: "alpha", Namespace: "default"},
		Spec:       enginev1alpha1.ShadowTestSpec{Mode: enginev1alpha1.ModeReplay},
		Status: enginev1alpha1.ShadowTestStatus{
			Phase:            enginev1alpha1.PhaseReady,
			Message:          "replay mode ready",
			BootStep:         enginev1alpha1.BootStepReady,
			CurrentSessionID: "session-42",
			ReplayState:      "started",
			KaiselPhase:      enginev1alpha1.CapturePhaseDisabled,
			Components: enginev1alpha1.ComponentStatus{
				IgrisReady:       true,
				ShopReady:        true,
				BeruReady:        true,
				KaiselRuleActive: false,
				AMQPBound:        true,
				TargetDeployment: "checkout-api",
				ShadowRolesReady: map[string]bool{
					monarchpb.RoleControlA:  true,
					monarchpb.RoleControlB:  true,
					monarchpb.RoleCandidate: false,
				},
			},
		},
	}

	u := ToStatusUpdate(st)

	if u.GetTestName() != "alpha" || u.GetNamespace() != "default" {
		t.Fatalf("identity = %s/%s", u.GetNamespace(), u.GetTestName())
	}
	if u.GetPhase() != monarchpb.Phase_PHASE_READY {
		t.Errorf("phase = %v", u.GetPhase())
	}
	if u.GetBootStep() != monarchpb.BootStep_BOOT_STEP_READY {
		t.Errorf("bootStep = %v", u.GetBootStep())
	}
	if u.GetMode() != monarchpb.Mode_MODE_REPLAY {
		t.Errorf("mode = %v", u.GetMode())
	}
	if u.GetKaiselPhase() != monarchpb.CapturePhase_CAPTURE_PHASE_DISABLED {
		t.Errorf("kaiselPhase = %v", u.GetKaiselPhase())
	}
	if u.GetMessage() != "replay mode ready" {
		t.Errorf("message = %q", u.GetMessage())
	}
	if u.GetCurrentSessionId() != "session-42" || u.GetReplayState() != "started" {
		t.Errorf("session/replay = %q/%q", u.GetCurrentSessionId(), u.GetReplayState())
	}
	c := u.GetComponents()
	if !c.GetIgrisReady() || !c.GetShopReady() || !c.GetBeruReady() || !c.GetAmqpBound() {
		t.Errorf("component flags lost: %+v", c)
	}
	if c.GetTargetDeployment() != "checkout-api" {
		t.Errorf("targetDeployment = %q", c.GetTargetDeployment())
	}
	roles := c.GetShadowRolesReady()
	if !roles[monarchpb.RoleControlA] || !roles[monarchpb.RoleControlB] || roles[monarchpb.RoleCandidate] {
		t.Errorf("shadowRolesReady = %v", roles)
	}
}

// TestToStatusUpdate_EveryConstantMaps catches a value added to the CR contract
// without a matching enum member: a silent UNSPECIFIED on the wire would render
// as a blank node rather than failing anywhere obvious.
func TestToStatusUpdate_EveryConstantMaps(t *testing.T) {
	for _, s := range []enginev1alpha1.BootStep{
		enginev1alpha1.BootStepValidating,
		enginev1alpha1.BootStepProvisioningSinks,
		enginev1alpha1.BootStepActivatingEgressTap,
		enginev1alpha1.BootStepBindingAMQP,
		enginev1alpha1.BootStepProvisioningShadow,
		enginev1alpha1.BootStepReady,
		enginev1alpha1.BootStepFailed,
	} {
		if got := toBootStep(s); got == monarchpb.BootStep_BOOT_STEP_UNSPECIFIED {
			t.Errorf("BootStep %q has no enum member", s)
		}
	}
	for _, s := range []string{
		enginev1alpha1.PhaseProgressing, enginev1alpha1.PhaseReady, enginev1alpha1.PhaseFailed,
		enginev1alpha1.PhaseDeleting, enginev1alpha1.PhaseDeleted,
	} {
		if got := toPhase(s); got == monarchpb.Phase_PHASE_UNSPECIFIED {
			t.Errorf("Phase %q has no enum member", s)
		}
	}
	for _, s := range []string{
		enginev1alpha1.CapturePhaseReady, enginev1alpha1.CapturePhaseDegraded, enginev1alpha1.CapturePhaseDisabled,
	} {
		if got := toCapturePhase(s); got == monarchpb.CapturePhase_CAPTURE_PHASE_UNSPECIFIED {
			t.Errorf("CapturePhase %q has no enum member", s)
		}
	}
	for _, s := range []string{enginev1alpha1.ModeRecord, enginev1alpha1.ModeReplay} {
		if got := toMode(s); got == monarchpb.Mode_MODE_UNSPECIFIED {
			t.Errorf("Mode %q has no enum member", s)
		}
	}
}

func TestToStatusUpdate_UnknownValuesDegradeToUnspecified(t *testing.T) {
	if got := toPhase("Bogus"); got != monarchpb.Phase_PHASE_UNSPECIFIED {
		t.Errorf("unknown phase = %v", got)
	}
	if got := toBootStep("Bogus"); got != monarchpb.BootStep_BOOT_STEP_UNSPECIFIED {
		t.Errorf("unknown bootStep = %v", got)
	}
}

// An empty spec.mode must stream as record, matching the CRD default.
func TestToStatusUpdate_EmptyModeDefaultsToRecord(t *testing.T) {
	st := &enginev1alpha1.ShadowTest{ObjectMeta: metav1.ObjectMeta{Name: "a", Namespace: "default"}}
	if got := ToStatusUpdate(st).GetMode(); got != monarchpb.Mode_MODE_RECORD {
		t.Fatalf("mode = %v, want MODE_RECORD", got)
	}
}

func TestToStatusUpdate_NilIsNil(t *testing.T) {
	if ToStatusUpdate(nil) != nil {
		t.Fatal("nil ShadowTest should convert to nil")
	}
}
