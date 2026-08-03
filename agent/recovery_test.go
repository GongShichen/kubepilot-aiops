package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/kubepilot-aiops/kubepilot/internal/domain"
	llm "github.com/kubepilot-aiops/kubepilot/internal/model"
)

func TestRecoveryCanonicalizesQualifiedTarget(t *testing.T) {
	arguments := json.RawMessage(`{"action":"restart_pod","target":"kubepilot-benchmark/gateway-service","parameters":{},"reason":"restore availability","risk":"brief disruption","diff":"rollout restart","rollback":"wait for prior replica","confidence":0.8}`)
	model := &scriptedDiagnosisModel{responses: []llm.Response{{ToolCalls: []llm.ToolCall{{Name: "submit_recovery", Arguments: arguments}}}}}
	incident := &domain.Incident{Namespace: "kubepilot-benchmark", Resource: "gateway-service"}
	if err := (RecoveryAgent{Model: model}).Propose(context.Background(), incident); err != nil {
		t.Fatal(err)
	}
	if incident.Proposal == nil || incident.Proposal.Target != "gateway-service" {
		t.Fatalf("proposal target was not canonicalized: %#v", incident.Proposal)
	}
}

func TestRecoveryRejectsAnotherNamespace(t *testing.T) {
	if _, err := canonicalProposalTarget("other/gateway-service", "kubepilot-benchmark", "gateway-service"); err == nil {
		t.Fatal("expected cross-namespace target to be rejected")
	}
}
