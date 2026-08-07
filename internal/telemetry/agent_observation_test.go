package telemetry

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/kubepilot-aiops/kubepilot/internal/domain"
)

func TestObserveAgentProjectsSelfReflectiveBrainQualityMetrics(t *testing.T) {
	now := time.Now().UTC()
	snapshot := domain.ExecutionSnapshot{SkillSnapshotHash: "skills", ModelConfigHash: "model", ToolSchemaHash: "tools", PolicyHash: "policy"}
	ref := domain.SkillRef{ID: "brain-kernel", Version: "1", ContentHash: "skill-hash"}
	h1 := domain.AgentHypothesis{ID: "h1", LineageID: "h1", Version: 1, Relation: domain.HypothesisRoot, Statement: "dependency failure", Category: "dependency", Mechanism: "timeout", Status: domain.HypothesisRefuted, CreatedAt: now}
	h2 := domain.AgentHypothesis{ID: "h2", LineageID: "h1", Version: 2, ParentIDs: []string{"h1"}, Relation: domain.HypothesisReplace, RevisionReason: "counterevidence", Statement: "resource pressure", Category: "resource", Mechanism: "cpu saturation", Status: domain.HypothesisSupported, LastValidatedAt: now, LastValidatedSnapshotHash: "evidence-snapshot", CreatedAt: now}
	grounding1 := domain.HypothesisGrounding{ID: "g1", HypothesisRevisionID: "h1", Level: domain.GroundingRefuted, ValidatedAt: now, EvidenceSnapshotHash: "evidence-snapshot"}
	grounding2 := domain.HypothesisGrounding{ID: "g2", HypothesisRevisionID: "h2", Level: domain.GroundingSupported, Evidence: domain.GroundingEvidence{SupportingEvidenceIDs: []string{"e1"}, IndependentSourceCount: 1}, ValidatedAt: now, EvidenceSnapshotHash: "evidence-snapshot"}
	incident := &domain.Incident{Evidence: []domain.Evidence{{ID: "e1", Source: "prometheus", Timestamp: now}}, Proposal: &domain.RecoveryProposal{ID: "p1"}}
	incident.Investigation = &domain.Investigation{
		Architecture:     "eino-native-self-reflective-brain",
		BrainTurns:       []domain.BrainTurn{{ID: "t1"}, {ID: "t2"}},
		SkillActivations: []domain.SkillActivation{{SkillID: ref.ID, Version: ref.Version, ContentHash: ref.ContentHash, Status: "ACTIVATED"}},
		ToolExecutions: []domain.BrainToolExecution{{
			Envelope: domain.AgentActionEnvelope{ToolCategory: domain.BrainToolEvidence, SkillRefs: []domain.SkillRef{ref}},
			Result:   domain.ToolResultRecord{Class: domain.ToolResultEvidence, NewInformation: true, Provenance: domain.ToolResultProvenance{ToolCallID: "call-1", ToolName: "query_prometheus_evidence", ToolSchemaHash: snapshot.ToolSchemaHash, Collector: "prometheus", WindowStart: now.Add(-time.Minute), WindowEnd: now, ObservedAt: now, RawArtifactHash: "artifact", ParserVersion: "v1", EvidenceIDs: []string{"e1"}}},
		}},
		AgentHypotheses:      []domain.AgentHypothesis{h1, h2},
		HypothesisAdmissions: []domain.HypothesisAdmission{{HypothesisRevisionID: "h1", Decision: "ADMITTED"}, {HypothesisRevisionID: "h2", Decision: "ADMITTED"}},
		HypothesisGroundings: []domain.HypothesisGrounding{grounding1, grounding2},
		Reflections:          []domain.ReflectionRecord{{Trigger: domain.ReflectionHypothesisRefuted, Accepted: true}},
		AgentDiagnosis:       &domain.AgentDiagnosis{HypothesisRevisionID: "h2", EvidenceIDs: []string{"e1"}, ValidationResultIDs: []string{"g2"}, EvidenceSnapshotHash: "evidence-snapshot", ExecutionSnapshot: snapshot, GroundingLevel: domain.GroundingSupported},
		ExecutionSnapshot:    &snapshot,
	}
	got := ObserveAgent(incident)
	if got.Iterations != 2 || got.ToolUses != 1 || got.HypothesisCorrectionOpportunities != 1 || got.HypothesisCorrections != 1 || got.GroundedHypothesisCorrections != 1 {
		t.Fatalf("unexpected Brain correction projection: %+v", got)
	}
	if !got.GroundedDecision || !got.GroundedAutomaticRecovery || got.UnsupportedDiagnosis || got.SkillDrift != 0 || got.IncompleteToolProvenance != 0 {
		t.Fatalf("unexpected Brain grounding/provenance projection: %+v", got)
	}
}

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

func TestObserveAgentCountsBaselineWorkerFindings(t *testing.T) {
	incident := &domain.Incident{
		RootCause: "payment memory leak",
		Investigation: &domain.Investigation{
			Findings:        []domain.WorkerFinding{{Worker: "metric", EvidenceIDs: []string{"m"}}, {Worker: "log", EvidenceIDs: []string{"l"}}, {Worker: "trace", EvidenceIDs: []string{"m", "t"}}},
			DiagnosisRounds: 2,
		},
	}
	got := ObserveAgent(incident)
	if got.EvidenceQueries != 3 || got.EvidenceEfficiency != 1.0/3.0 || got.IndependentEvidenceRequests != 3 || got.NewEvidenceIDs != 3 || got.ConvergenceRounds != 2 {
		t.Fatalf("baseline evidence work was not observed: %+v", got)
	}
}

func TestObserveAgentClassifiesCognitivePolicyOutcomes(t *testing.T) {
	incident := &domain.Incident{Investigation: &domain.Investigation{
		CognitiveReasoning: []domain.CognitiveReasoning{{InvestigationPolicies: []domain.InvestigationPolicy{{Status: "useful"}, {Status: "ineffective_no_decision_change"}, {Status: "rejected_low_diagnostic_value"}}}},
		ExpansionRequests:  []domain.CandidateExpansionRequest{{Status: "activated_non_actionable"}},
	}}
	got := ObserveAgent(incident)
	if got.CognitiveProposals != 4 || got.CognitiveAcceptedProposals != 3 || got.CognitiveUsefulProposals != 1 || got.CognitiveRejectedProposals != 1 {
		t.Fatalf("cognitive proposal outcomes were not observed: %+v", got)
	}
}

func TestObserveAgentProjectsGateAuditAndModelUsage(t *testing.T) {
	incident := &domain.Incident{Investigation: &domain.Investigation{
		Architecture: "eino-cognitive-diagnosis-runtime",
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
		t.Fatalf("baseline model audit was not projected: %+v", got)
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
