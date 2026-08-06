package safety

import (
	"strings"
	"testing"

	"github.com/kubepilot-aiops/kubepilot/internal/domain"
)

func TestSafetyFeedbackHasScopeWithoutPrescribingTool(t *testing.T) {
	feedback := Repairable(domain.SafetyScopeDiagnosis, "insufficient_support", "the current hypothesis lacks independent corroboration", []string{"at least two current evidence sources are required"}, []string{"additional independent observations are required"}, 2)
	if feedback.Scope != domain.SafetyScopeDiagnosis || !ValidateFeedback(feedback, []string{"query_prometheus_evidence"}) {
		t.Fatalf("safe scoped feedback rejected: %+v", feedback)
	}
	feedback.RequiredCapabilities = []string{"call query_prometheus_evidence next"}
	if ValidateFeedback(feedback, []string{"query_prometheus_evidence"}) {
		t.Fatal("feedback prescribed a concrete tool")
	}
}

func TestSafetyFeedbackTruncationPreservesUTF8(t *testing.T) {
	feedback := Fatal(domain.SafetyScopeRecoveryProposal, "policy_violation", strings.Repeat("故障", 400))
	if len([]rune(feedback.Reason)) != 512 {
		t.Fatalf("unexpected bounded reason length: %d", len([]rune(feedback.Reason)))
	}
}

func TestHumanAndAllowedFeedbackRemainStructured(t *testing.T) {
	human := HumanRequired(domain.SafetyScopeRecoveryProposal, "operator_required", " explicit operator decision required ")
	if !human.RequiresHuman || human.Reason != "explicit operator decision required" || !ValidateFeedback(human, nil) {
		t.Fatalf("human feedback=%+v", human)
	}
	allowed := Allowed(domain.SafetyScopeDiagnosis)
	if !allowed.Allowed || !ValidateFeedback(allowed, nil) {
		t.Fatalf("allowed feedback=%+v", allowed)
	}
}
