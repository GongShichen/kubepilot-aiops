package telemetry

import (
	"sort"
	"strings"

	"github.com/kubepilot-aiops/kubepilot/internal/domain"
)

// AgentObservation is the production-side, evaluator-neutral projection of an
// Incident's Agent runtime behavior. Benchmark code consumes this projection;
// it does not reconstruct Agent semantics from persistence internals.
type AgentObservation struct {
	Architecture                      string
	Iterations                        int
	ToolUses                          int
	ToolCost                          int
	Tokens                            int
	Corrections                       int
	SafetyRejections                  int
	SelfCorrectionAttempts            int
	SelfCorrectionSucceeded           bool
	HypothesisCount                   int
	HypothesisConverged               bool
	EvidenceQueries                   int
	EvidenceEfficiency                float64
	IndependentEvidenceRequests       int
	NewEvidenceIDs                    int
	ConvergenceRounds                 int
	CognitiveProposals                int
	CognitiveAcceptedProposals        int
	CognitiveUsefulProposals          int
	CognitiveRejectedProposals        int
	HypothesisCorrectionOpportunities int
	HypothesisCorrections             int
	GroundedHypothesisCorrections     int
	GroundedDecision                  bool
	AutomaticRecoveryDiagnosis        bool
	GroundedAutomaticRecovery         bool
	NonControlToolResults             int
	InformativeToolResults            int
	ReflectionTriggers                int
	AcceptedReflections               int
	SkillActivations                  int
	AcceptedSkillActivations          int
	SkillDrift                        int
	HypothesisAdmissions              int
	AcceptedHypothesisAdmissions      int
	GroundableHypothesisAdmissions    int
	UnsupportedDiagnosis              bool
	IncompleteToolProvenance          int
	ConfidenceUpdates                 int
	AttributedEvidence                int
	TopologyCandidates                int
	PlannerTasks                      int
	WorkerFindings                    int
	DebateRounds                      int
	MemoryReads                       int
	InputTokens                       int
	OutputTokens                      int
	ReasoningTokens                   int
	EstimatedModelCost                float64
	ArbitrationGateFailures           []string
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
		investigation := incident.Investigation
		observation.Architecture = investigation.Architecture
		observation.PlannerTasks = len(investigation.Plan.Tasks)
		observation.WorkerFindings = len(investigation.Findings)
		observation.DebateRounds = len(investigation.Debate)
		observation.MemoryReads = len(investigation.MemoryReads)
		if investigation.Architecture == "eino-native-self-reflective-brain" {
			observeBrainAudit(&observation, incident, investigation)
		}
		// Hierarchical workers call their scoped collectors through the server,
		// not through the legacy Diagnosis ReAct decision ledger. Count each
		// completed worker query so evidence-efficiency ablations do not collapse
		// to zero for the hierarchical strategy.
		observation.EvidenceQueries += len(investigation.Findings)
		observation.IndependentEvidenceRequests += len(investigation.Findings)
		seenEvidence := map[string]bool{}
		for _, finding := range investigation.Findings {
			for _, evidenceID := range finding.EvidenceIDs {
				seenEvidence[evidenceID] = true
			}
		}
		if investigation.Architecture != "eino-native-self-reflective-brain" {
			observation.NewEvidenceIDs = len(seenEvidence)
		}
		observation.ConvergenceRounds = investigation.DiagnosisRounds
		if observation.ConvergenceRounds == 0 && len(investigation.Plan.Tasks) > 0 {
			observation.ConvergenceRounds = 1
		}
		for _, reasoning := range investigation.CognitiveReasoning {
			for _, policy := range reasoning.InvestigationPolicies {
				observation.CognitiveProposals++
				switch policy.Status {
				case "useful":
					observation.CognitiveAcceptedProposals++
					observation.CognitiveUsefulProposals++
				case "accepted", "ineffective_no_new_evidence", "ineffective_no_decision_change":
					observation.CognitiveAcceptedProposals++
				default:
					observation.CognitiveRejectedProposals++
				}
			}
		}
		for _, expansion := range investigation.ExpansionRequests {
			observation.CognitiveProposals++
			if expansion.Status == "activated_non_actionable" {
				observation.CognitiveAcceptedProposals++
			} else {
				observation.CognitiveRejectedProposals++
			}
		}
		for _, usage := range investigation.ModelUsage {
			observation.InputTokens += usage.InputTokens
			observation.OutputTokens += usage.OutputTokens
			observation.ReasoningTokens += usage.ReasoningTokens
			observation.EstimatedModelCost += usage.EstimatedCost
		}
		if investigation.Arbitration != nil {
			seenGates := map[string]bool{}
			for _, result := range investigation.Arbitration.GateResults {
				for _, gate := range result.FailedGates {
					if !seenGates[gate] {
						seenGates[gate] = true
						observation.ArbitrationGateFailures = append(observation.ArbitrationGateFailures, gate)
					}
				}
			}
			sort.Strings(observation.ArbitrationGateFailures)
		}
	}
	ledger := incident.DiagnosisLedger
	isBrain := incident.Investigation != nil && incident.Investigation.Architecture == "eino-native-self-reflective-brain"
	if ledger != nil {
		for _, feedback := range ledger.SafetyFeedback {
			if !feedback.Allowed {
				observation.SafetyRejections++
			}
		}
		if !isBrain {
			observation.HypothesisCount = len(ledger.Drafts)
			observation.HypothesisConverged = hypothesisConverged(ledger)
		}
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
	if !isBrain {
		observation.SelfCorrectionAttempts = observation.Corrections
		observation.SelfCorrectionSucceeded = observation.Corrections > 0 && observation.HypothesisConverged
	}
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

func observeBrainAudit(observation *AgentObservation, incident *domain.Incident, investigation *domain.Investigation) {
	if observation == nil || incident == nil || investigation == nil {
		return
	}
	observation.Iterations = len(investigation.BrainTurns)
	observation.ToolUses = len(investigation.ToolExecutions)
	observation.HypothesisCount = len(investigation.AgentHypotheses)
	observation.ConfidenceUpdates = len(investigation.BeliefDeltas)
	observation.ReflectionTriggers = len(investigation.Reflections)
	for _, reflection := range investigation.Reflections {
		if reflection.Accepted {
			observation.AcceptedReflections++
		}
	}
	observation.Corrections = len(investigation.BeliefDeltas)
	for _, hypothesis := range investigation.AgentHypotheses {
		if hypothesis.Relation != domain.HypothesisRoot {
			observation.Corrections++
		}
	}
	observation.SkillActivations = len(investigation.SkillActivations)
	activatedSkills := map[string]bool{}
	for _, activation := range investigation.SkillActivations {
		if activation.Status == "ACTIVATED" {
			observation.AcceptedSkillActivations++
			activatedSkills[activation.SkillID+"@"+activation.Version+"#"+activation.ContentHash] = true
		}
	}
	seenEvidence := map[string]bool{}
	for _, execution := range investigation.ToolExecutions {
		if execution.Envelope.ToolCategory != domain.BrainToolControl {
			observation.NonControlToolResults++
			if execution.Result.NewInformation {
				observation.InformativeToolResults++
			}
		}
		if execution.Envelope.ToolCategory == domain.BrainToolEvidence {
			observation.EvidenceQueries++
			observation.IndependentEvidenceRequests++
		}
		if execution.Envelope.ToolCategory == domain.BrainToolRetrieval {
			observation.MemoryReads++
		}
		for _, evidenceID := range execution.Result.Provenance.EvidenceIDs {
			seenEvidence[evidenceID] = true
		}
		if !completeToolProvenance(execution.Result) {
			observation.IncompleteToolProvenance++
		}
		for _, ref := range execution.Envelope.SkillRefs {
			if !activatedSkills[ref.ID+"@"+ref.Version+"#"+ref.ContentHash] {
				observation.SkillDrift++
			}
		}
	}
	observation.NewEvidenceIDs = len(seenEvidence)
	observation.HypothesisAdmissions = len(investigation.HypothesisAdmissions)
	groundings := make(map[string]domain.HypothesisGrounding, len(investigation.HypothesisGroundings))
	for _, grounding := range investigation.HypothesisGroundings {
		groundings[grounding.HypothesisRevisionID] = grounding
	}
	for _, admission := range investigation.HypothesisAdmissions {
		if admission.Decision != "ADMITTED" {
			continue
		}
		observation.AcceptedHypothesisAdmissions++
		if grounding, ok := groundings[admission.HypothesisRevisionID]; ok && (grounding.Level == domain.GroundingSupported || grounding.Level == domain.GroundingPartial) {
			observation.GroundableHypothesisAdmissions++
		}
	}
	refuted := map[string]bool{}
	for _, grounding := range investigation.HypothesisGroundings {
		if grounding.Level == domain.GroundingRefuted {
			refuted[grounding.HypothesisRevisionID] = true
		}
	}
	observation.HypothesisCorrectionOpportunities = len(refuted)
	hypotheses := make(map[string]domain.AgentHypothesis, len(investigation.AgentHypotheses))
	correctedRefuted := map[string]bool{}
	for _, hypothesis := range investigation.AgentHypotheses {
		hypotheses[hypothesis.ID] = hypothesis
		for _, parentID := range hypothesis.ParentIDs {
			if refuted[parentID] {
				correctedRefuted[parentID] = true
			}
		}
	}
	observation.HypothesisCorrections = len(correctedRefuted)
	if diagnosis := investigation.AgentDiagnosis; diagnosis != nil {
		selected, selectedOK := hypotheses[diagnosis.HypothesisRevisionID]
		selectedGrounding, groundedOK := groundings[diagnosis.HypothesisRevisionID]
		observation.HypothesisConverged = selectedOK && groundedOK && selectedGrounding.Level == domain.GroundingSupported
		observation.UnsupportedDiagnosis = !groundedOK || selectedGrounding.Level == domain.GroundingUnknown || selectedGrounding.Level == domain.GroundingRefuted || len(diagnosis.EvidenceIDs) == 0 || len(diagnosis.ValidationResultIDs) == 0
		observation.GroundedDecision = groundedDecision(incident, investigation, selected, selectedGrounding, selectedOK && groundedOK)
		if observation.HypothesisCorrections > 0 && observation.GroundedDecision && hasRefutedAncestor(selected.ID, hypotheses, refuted, map[string]bool{}) {
			observation.GroundedHypothesisCorrections++
		}
	}
	observation.AutomaticRecoveryDiagnosis = incident.Proposal != nil || incident.RecoveryExecution != nil
	observation.GroundedAutomaticRecovery = observation.AutomaticRecoveryDiagnosis && observation.GroundedDecision
	observation.SelfCorrectionAttempts = observation.HypothesisCorrectionOpportunities
	observation.SelfCorrectionSucceeded = observation.GroundedHypothesisCorrections > 0
	if observation.ConvergenceRounds == 0 && len(investigation.HypothesisGroundings) > 0 {
		observation.ConvergenceRounds = len(investigation.HypothesisGroundings)
	}
}

func completeToolProvenance(result domain.ToolResultRecord) bool {
	p := result.Provenance
	if p.ToolCallID == "" || p.ToolName == "" || p.ToolSchemaHash == "" || p.Collector == "" || p.WindowStart.IsZero() || p.WindowEnd.IsZero() || p.ObservedAt.IsZero() || p.RawArtifactHash == "" || p.ParserVersion == "" {
		return false
	}
	if result.Class == domain.ToolResultEvidence {
		return len(p.EvidenceIDs) > 0
	}
	if result.Class == domain.ToolResultStateChange && result.Status != "DRY_RUN_SUCCEEDED" {
		return p.StateChangeID != "" && p.ApprovalID != "" && p.MutationSpecHash != "" && p.TargetUID != "" && p.ResourceVersion != ""
	}
	return true
}

func groundedDecision(incident *domain.Incident, investigation *domain.Investigation, selected domain.AgentHypothesis, grounding domain.HypothesisGrounding, found bool) bool {
	diagnosis := investigation.AgentDiagnosis
	if !found || diagnosis == nil || selected.LineageID == "" || len(diagnosis.EvidenceIDs) == 0 || len(diagnosis.ValidationResultIDs) == 0 {
		return false
	}
	if diagnosis.EvidenceSnapshotHash == "" || diagnosis.EvidenceSnapshotHash != selected.LastValidatedSnapshotHash || diagnosis.EvidenceSnapshotHash != grounding.EvidenceSnapshotHash {
		return false
	}
	if investigation.ExecutionSnapshot == nil || diagnosis.ExecutionSnapshot != *investigation.ExecutionSnapshot {
		return false
	}
	validationFound := false
	for _, id := range diagnosis.ValidationResultIDs {
		if id == grounding.ID {
			validationFound = true
		}
	}
	if !validationFound || !completeLineage(selected.ID, investigation.AgentHypotheses, map[string]bool{}) {
		return false
	}
	evidenceExists, provenance := map[string]bool{}, map[string]bool{}
	for _, item := range incident.Evidence {
		evidenceExists[item.ID] = item.ID != ""
	}
	for _, execution := range investigation.ToolExecutions {
		if execution.Result.Class != domain.ToolResultEvidence || !completeToolProvenance(execution.Result) {
			continue
		}
		for _, id := range execution.Result.Provenance.EvidenceIDs {
			provenance[id] = true
		}
	}
	for _, id := range diagnosis.EvidenceIDs {
		if !evidenceExists[id] || !provenance[id] {
			return false
		}
	}
	return true
}

func completeLineage(id string, hypotheses []domain.AgentHypothesis, visiting map[string]bool) bool {
	byID := make(map[string]domain.AgentHypothesis, len(hypotheses))
	for _, hypothesis := range hypotheses {
		byID[hypothesis.ID] = hypothesis
	}
	var visit func(string) bool
	visit = func(current string) bool {
		if visiting[current] {
			return false
		}
		hypothesis, ok := byID[current]
		if !ok {
			return false
		}
		if hypothesis.Relation == domain.HypothesisRoot {
			return len(hypothesis.ParentIDs) == 0
		}
		if len(hypothesis.ParentIDs) == 0 {
			return false
		}
		visiting[current] = true
		defer delete(visiting, current)
		for _, parentID := range hypothesis.ParentIDs {
			if !visit(parentID) {
				return false
			}
		}
		return true
	}
	return visit(id)
}

func hasRefutedAncestor(id string, hypotheses map[string]domain.AgentHypothesis, refuted, visiting map[string]bool) bool {
	if visiting[id] {
		return false
	}
	visiting[id] = true
	defer delete(visiting, id)
	for _, parentID := range hypotheses[id].ParentIDs {
		if refuted[parentID] || hasRefutedAncestor(parentID, hypotheses, refuted, visiting) {
			return true
		}
	}
	return false
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
