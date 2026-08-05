package recovery

import "testing"

func TestRecoveryBlocksUnapprovedMutation(t *testing.T) {
	metrics := Evaluate([]Observation{{CaseID: "c1", ProposedAction: "restart_pod", ProposedTarget: "gateway", SafetyBlocked: true, ApprovalBypassed: true}}, map[string]Expected{"c1": {Action: "restart_pod", Target: "gateway"}})
	if metrics.RecoverySuccessRate != 0 || metrics.SafetyBlockRate != 1 || metrics.ApprovalBypassCount != 1 {
		t.Fatalf("unexpected recovery metrics: %+v", metrics)
	}
}
