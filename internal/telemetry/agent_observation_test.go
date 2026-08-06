package telemetry

import (
	"context"
	"slices"
	"testing"

	"github.com/kubepilot-aiops/kubepilot/internal/domain"
)

func TestObserveAgentProjectsRuntimeState(t *testing.T) {
	incident := &domain.Incident{
		RootCause: "database pool exhausted",
		AgentBudget: &domain.AgentBudgetState{
			IncidentUses: 7, IncidentCost: 11, IncidentTokens: 900,
			Usage: map[string]domain.AgentBudgetUsage{"diagnosis": {Iterations: 4, Corrections: 1}},
		},
		Evidence: []domain.Evidence{{Attribution: &domain.EvidenceAttribution{AttributionScore: .9}}},
		DiagnosisLedger: &domain.DiagnosisLedger{
			SelectedHypothesisID: "h1",
			Drafts:               []domain.HypothesisDraft{{ID: "h1"}},
			Verified: []domain.VerifiedHypothesis{{
				Draft: domain.HypothesisDraft{ID: "h1"}, Status: domain.HypothesisAccepted,
				ConfidenceHistory: []domain.HypothesisConfidenceRecord{{Sequence: 1}},
			}},
			SafetyFeedback: []domain.SafetyFeedback{{Allowed: false}},
			AgentDecisions: []domain.AgentDecisionEvent{{SelectedAction: "query_loki_evidence"}},
			Candidates:     []domain.RetrievalCandidate{{Rank: domain.RankBreakdown{TopologyScore: .8}}},
		},
	}
	got := ObserveAgent(incident)
	if got.Iterations != 4 || got.ToolUses != 7 || got.ToolCost != 11 || got.Tokens != 900 || got.Corrections != 1 {
		t.Fatalf("unexpected budget observation: %+v", got)
	}
	if !got.HypothesisConverged || !got.SelfCorrectionSucceeded || got.EvidenceQueries != 1 || got.TopologyCandidates != 1 || got.AttributedEvidence != 1 {
		t.Fatalf("unexpected reasoning observation: %+v", got)
	}
}

func TestObserveAgentCountsHierarchicalWorkerFindings(t *testing.T) {
	incident := &domain.Incident{
		RootCause: "payment memory leak",
		Investigation: &domain.Investigation{
			Findings: []domain.WorkerFinding{{Worker: "metric"}, {Worker: "log"}, {Worker: "trace"}},
		},
	}
	got := ObserveAgent(incident)
	if got.EvidenceQueries != 3 || got.EvidenceEfficiency != 1.0/3.0 {
		t.Fatalf("hierarchical evidence work was not observed: %+v", got)
	}
}

func TestObserveAgentProjectsGateAuditAndModelUsage(t *testing.T) {
	incident := &domain.Incident{Investigation: &domain.Investigation{
		Architecture: "hierarchical-causal-react",
		Plan:         domain.InvestigationPlan{Tasks: []domain.WorkerTask{{ID: "metric"}}},
		Findings:     []domain.WorkerFinding{{Worker: "metric"}},
		Debate:       []domain.DebateRound{{Round: 1}},
		MemoryReads:  []domain.MemoryAccessEvent{{QueryHash: "memory"}},
		ModelUsage:   []domain.ModelUsageEvent{{InputTokens: 10, OutputTokens: 20, ReasoningTokens: 5, EstimatedCost: .01}},
		Arbitration: &domain.ArbitrationResult{GateResults: []domain.HypothesisGateResult{
			{HypothesisID: "h1", FailedGates: []string{"final_score", "supporting_score"}},
			{HypothesisID: "h2", FailedGates: []string{"final_score"}},
		}},
	}}
	got := ObserveAgent(incident)
	if got.Architecture == "" || got.PlannerTasks != 1 || got.DebateRounds != 1 || got.MemoryReads != 1 || got.InputTokens != 10 || got.OutputTokens != 20 || got.ReasoningTokens != 5 || got.EstimatedModelCost != .01 {
		t.Fatalf("hierarchical model audit was not projected: %+v", got)
	}
	if len(got.ArbitrationGateFailures) != 2 || !slices.Contains(got.ArbitrationGateFailures, "final_score") || !slices.Contains(got.ArbitrationGateFailures, "supporting_score") {
		t.Fatalf("gate failures were not deduplicated: %+v", got.ArbitrationGateFailures)
	}
}

func TestInitWithoutExporterIsNoop(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	shutdown, err := Init(context.Background(), "test")
	if err != nil || shutdown == nil || shutdown(context.Background()) != nil {
		t.Fatalf("no-op telemetry initialization failed: shutdown_nil=%t err=%v", shutdown == nil, err)
	}
}
