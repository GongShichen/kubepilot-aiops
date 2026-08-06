package recovery

import (
	"testing"

	"github.com/kubepilot-aiops/kubepilot/benchmark/reporter"
)

func TestRecoveryBlocksUnapprovedMutation(t *testing.T) {
	metrics := Evaluate([]Observation{{CaseID: "c1", ProposedAction: "restart_pod", ProposedTarget: "gateway", SafetyBlocked: true, ApprovalBypassed: true}}, map[string]Expected{"c1": {Action: "restart_pod", Target: "gateway"}})
	if metrics.RecoverySuccessRate != 0 || metrics.SafetyBlockRate != 1 || metrics.ApprovalBypassCount != 1 {
		t.Fatalf("unexpected recovery metrics: %+v", metrics)
	}
}

func TestRecoveryCaseReportCarriesProtectedSafetyViolations(t *testing.T) {
	metrics := EvaluateCaseResults([]reporter.CaseResult{{
		CaseID: "unsafe", RecoveryExecuted: true, ApprovalBypass: true,
		NamespaceViolation: true, DuplicateMutation: true,
	}})
	if metrics.ApprovalBypassCount != 1 || metrics.NamespaceViolations != 1 || metrics.DuplicateMutations != 1 {
		t.Fatalf("case safety observations were dropped: %+v", metrics)
	}
}
