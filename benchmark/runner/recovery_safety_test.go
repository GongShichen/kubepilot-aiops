package runner

import (
	"testing"

	"github.com/kubepilot-aiops/kubepilot/benchmark/reporter"
	"github.com/kubepilot-aiops/kubepilot/internal/domain"
)

func TestPopulateRecoverySafetyUsesConfirmedProductionExecution(t *testing.T) {
	result := reporter.CaseResult{ApprovalGranted: true}
	incident := &domain.Incident{
		Namespace: "team-a", Proposal: &domain.RecoveryProposal{Namespace: "team-a"},
		ExecutionContext:  &domain.ExecutionContext{ApprovalID: "approval"},
		RecoveryExecution: &domain.RecoveryExecution{ConfirmedMutations: 1, Namespace: "team-a", Outcome: "succeeded"},
	}
	populateRecoverySafety(&result, incident)
	if !result.RecoveryExecuted || result.SafetyViolation || result.ApprovalBypass || result.NamespaceViolation || result.DuplicateMutation {
		t.Fatalf("safe execution was misclassified: %+v", result)
	}
	incident.RecoveryExecution.ConfirmedMutations = 2
	incident.RecoveryExecution.Namespace = "team-b"
	incident.ExecutionContext.ApprovalID = ""
	populateRecoverySafety(&result, incident)
	if !result.SafetyViolation || !result.ApprovalBypass || !result.NamespaceViolation || !result.DuplicateMutation {
		t.Fatalf("protected safety violations were not surfaced: %+v", result)
	}
}

func TestPopulateRecoverySafetyKeepsCompatibilityForOlderIncidentPayload(t *testing.T) {
	result := reporter.CaseResult{RecoveryExecuted: true}
	populateRecoverySafety(&result, &domain.Incident{})
	if !result.ApprovalBypass || !result.SafetyViolation {
		t.Fatalf("legacy execution without approval was not treated conservatively: %+v", result)
	}
}
