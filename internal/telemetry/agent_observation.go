package telemetry

import (
	"strings"

	"github.com/kubepilot-aiops/kubepilot/internal/domain"
)

// AgentObservation is the production-side, evaluator-neutral projection of an
// Incident's Agent runtime behavior. Benchmark code consumes this projection;
// it does not reconstruct Agent semantics from persistence internals.
type AgentObservation struct {
	Architecture            string
	Iterations              int
	ToolUses                int
	ToolCost                int
	Tokens                  int
	Corrections             int
	SafetyRejections        int
	SelfCorrectionAttempts  int
	SelfCorrectionSucceeded bool
	HypothesisCount         int
	HypothesisConverged     bool
	EvidenceQueries         int
	EvidenceEfficiency      float64
	ConfidenceUpdates       int
	AttributedEvidence      int
	TopologyCandidates      int
	PlannerTasks            int
	WorkerFindings          int
	DebateRounds            int
	MemoryReads             int
	InputTokens             int
	OutputTokens            int
	ReasoningTokens         int
	EstimatedModelCost      float64
}

// ObserveAgent projects auditable runtime state without reading evaluator
// labels or benchmark data.
func ObserveAgent(incident *domain.Incident) AgentObservation {
	var observation AgentObservation
	if incident == nil {
		return observation
	}
	if incident.AgentBudget != nil {
		observation.ToolUses = incident.AgentBudget.IncidentUses
		observation.ToolCost = incident.AgentBudget.IncidentCost
		observation.Tokens = incident.AgentBudget.IncidentTokens
		for _, usage := range incident.AgentBudget.Usage {
			observation.Iterations += usage.Iterations
			observation.Corrections += usage.Corrections
		}
	}
	if incident.Investigation != nil {
		observation.Architecture = incident.Investigation.Architecture
		observation.PlannerTasks = len(incident.Investigation.Plan.Tasks)
		observation.WorkerFindings = len(incident.Investigation.Findings)
		observation.DebateRounds = len(incident.Investigation.Debate)
		observation.MemoryReads = len(incident.Investigation.MemoryReads)
		// Hierarchical workers call their scoped collectors through the server,
		// not through the legacy Diagnosis ReAct decision ledger. Count each
		// completed worker query so evidence-efficiency ablations do not collapse
		// to zero for the hierarchical strategy.
		observation.EvidenceQueries += len(incident.Investigation.Findings)
		for _, usage := range incident.Investigation.ModelUsage {
			observation.InputTokens += usage.InputTokens
			observation.OutputTokens += usage.OutputTokens
			observation.ReasoningTokens += usage.ReasoningTokens
			observation.EstimatedModelCost += usage.EstimatedCost
		}
	}
	ledger := incident.DiagnosisLedger
	if ledger != nil {
		for _, feedback := range ledger.SafetyFeedback {
			if !feedback.Allowed {
				observation.SafetyRejections++
			}
		}
		observation.HypothesisCount = len(ledger.Drafts)
		observation.HypothesisConverged = hypothesisConverged(ledger)
		for _, decision := range ledger.AgentDecisions {
			if isEvidenceQuery(decision.SelectedAction) {
				observation.EvidenceQueries++
			}
		}
		for _, hypothesis := range ledger.Verified {
			observation.ConfidenceUpdates += len(hypothesis.ConfidenceHistory)
		}
		for _, candidate := range ledger.Candidates {
			if candidate.Rank.TopologySimilarity > 0 || candidate.Rank.TopologyScore > 0 || candidate.SourceRanks["topology"] > 0 {
				observation.TopologyCandidates++
			}
		}
	}
	observation.SelfCorrectionAttempts = observation.Corrections
	observation.SelfCorrectionSucceeded = observation.Corrections > 0 && observation.HypothesisConverged
	if observation.EvidenceQueries > 0 && incident.RootCause != "" {
		observation.EvidenceEfficiency = 1 / float64(observation.EvidenceQueries)
	}
	for _, evidence := range incident.Evidence {
		if evidence.Attribution != nil {
			observation.AttributedEvidence++
		}
	}
	return observation
}

func hypothesisConverged(ledger *domain.DiagnosisLedger) bool {
	if ledger == nil || ledger.SelectedHypothesisID == "" {
		return false
	}
	for _, verified := range ledger.Verified {
		if verified.Draft.ID == ledger.SelectedHypothesisID && verified.Status == domain.HypothesisAccepted && len(verified.ConfidenceHistory) > 0 {
			return true
		}
	}
	return false
}

func isEvidenceQuery(action string) bool {
	return strings.HasPrefix(action, "query_") || strings.HasPrefix(action, "retrieve_") || strings.HasPrefix(action, "load_") || strings.Contains(action, "evidence")
}
