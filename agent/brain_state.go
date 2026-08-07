package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/cloudwego/eino/schema"
	"github.com/kubepilot-aiops/kubepilot/internal/brainruntime"
	"github.com/kubepilot-aiops/kubepilot/internal/domain"
	"github.com/kubepilot-aiops/kubepilot/internal/topology"
	"github.com/kubepilot-aiops/kubepilot/reasoning"
	"github.com/oklog/ulid/v2"
)

func (r *brainGraphRuntime) classifyToolResults(ctx context.Context, messages []*schema.Message) (*WorkflowState, error) {
	state, err := brainWorkflowState(ctx)
	if err != nil {
		return nil, err
	}
	for _, message := range messages {
		if message == nil || message.Role != schema.Tool {
			continue
		}
		output, decodeErr := decodeBrainCapabilityOutput(message.Content)
		envelope := envelopeFromToolCall(state, message.ToolCallID, message.ToolName)
		if decodeErr != nil {
			output = errorBrainOutput(envelope, "tool result violated its structured output contract: "+decodeErr.Error(), false)
		}
		output.Provenance.ToolCallID = message.ToolCallID
		output.Provenance.ToolName = message.ToolName
		output.Provenance.ToolSchemaHash = state.ExecutionSnapshot.ToolSchemaHash
		if output.Provenance.ObservedAt.IsZero() {
			output.Provenance.ObservedAt = time.Now().UTC()
		}
		if output.Provenance.WindowStart.IsZero() {
			output.Provenance.WindowStart = output.Provenance.ObservedAt
		}
		if output.Provenance.WindowEnd.IsZero() {
			output.Provenance.WindowEnd = output.Provenance.ObservedAt
		}
		result := domain.ToolResultRecord{Class: output.Class, Provenance: output.Provenance, Status: output.Status, Summary: output.Summary, NewInformation: output.NewInformation, ConstraintCode: output.ConstraintCode, Infrastructure: output.Infrastructure, OccurredAt: time.Now().UTC()}
		state.ToolExecutions = append(state.ToolExecutions, domain.BrainToolExecution{Envelope: envelope, Result: result})
		state.BrainBudget.Usage.ToolCalls++
		toolMessage := *message
		toolMessage.ReasoningContent = ""
		// Persist the server-classified result, not the raw adapter payload. This
		// makes Class, Status, failure semantics, complete Provenance and Evidence
		// IDs visible to the next Brain turn as one auditable Tool result.
		classified, marshalErr := json.Marshal(output)
		if marshalErr != nil {
			return nil, fmt.Errorf("encode classified Brain Tool result: %w", marshalErr)
		}
		toolMessage.Content = string(classified)
		state.BrainMessages = append(state.BrainMessages, &toolMessage)
		r.applyCapabilityOutput(state, output)
	}
	r.syncInvestigation(state)
	return state, nil
}

func (r *brainGraphRuntime) applyCapabilityOutput(state *WorkflowState, output brainCapabilityOutput) {
	if len(output.Evidence) > 0 {
		state.Incident.Evidence = mergeEvidence(state.Incident.Evidence, output.Evidence)
	}
	if len(output.Patterns) > 0 {
		state.CausalPatterns = append([]domain.CausalPattern(nil), output.Patterns...)
	}
	if len(output.Candidates) > 0 || len(output.Patterns) > 0 {
		r.auditRetrieval(state, output)
	}
	for _, hypothesis := range output.Hypotheses {
		if hypothesis.Relation != domain.HypothesisRoot {
			for _, parentID := range hypothesis.ParentIDs {
				for index := range state.AgentHypotheses {
					if state.AgentHypotheses[index].ID != parentID {
						continue
					}
					if hypothesis.Relation == domain.HypothesisMerge {
						state.AgentHypotheses[index].Status = domain.HypothesisMerged
					} else {
						state.AgentHypotheses[index].Status = domain.HypothesisReplaced
					}
				}
			}
		}
		state.AgentHypotheses = append(state.AgentHypotheses, hypothesis)
	}
	if len(output.Hypotheses) > 0 {
		state.BrainBudget.Usage.ActiveHypotheses = activeHypothesisCount(state.AgentHypotheses)
		state.BrainBudget.Usage.HypothesisBranches = hypothesisBranchCount(state.AgentHypotheses)
	}
	state.HypothesisAdmissions = append(state.HypothesisAdmissions, output.Admissions...)
	if output.Grounding != nil {
		state.HypothesisGroundings = replaceGrounding(state.HypothesisGroundings, *output.Grounding)
		for index := range state.AgentHypotheses {
			if state.AgentHypotheses[index].ID != output.Grounding.HypothesisRevisionID {
				continue
			}
			state.AgentHypotheses[index].LastValidatedAt = output.Grounding.ValidatedAt
			state.AgentHypotheses[index].LastValidatedSnapshotHash = output.Grounding.EvidenceSnapshotHash
			switch output.Grounding.Level {
			case domain.GroundingSupported:
				state.AgentHypotheses[index].Status = domain.HypothesisSupported
			case domain.GroundingRefuted:
				state.AgentHypotheses[index].Status = domain.HypothesisRefuted
			default:
				state.AgentHypotheses[index].Status = domain.HypothesisInvestigating
			}
		}
	}
	if output.GroundingDelta != nil {
		state.GroundingDeltas = append(state.GroundingDeltas, *output.GroundingDelta)
	}
	if output.BeliefDelta != nil {
		state.BeliefDeltas = append(state.BeliefDeltas, *output.BeliefDelta)
		for index := range state.AgentHypotheses {
			if state.AgentHypotheses[index].ID == output.BeliefDelta.HypothesisRevisionID && output.BeliefDelta.Committed {
				state.AgentHypotheses[index].ModelConfidence = output.BeliefDelta.NewConfidence
			}
		}
	}
	if output.Understanding != nil {
		state.IncidentUnderstanding = output.Understanding
		state.BrainPhase, state.ActiveToolCategory = domain.BrainPhasePlanning, domain.BrainToolReasoning
	}
	if output.InvestigationPlan != nil {
		state.Incident.Investigation.Plan = *output.InvestigationPlan
		state.BrainPhase, state.ActiveToolCategory = domain.BrainPhaseInvestigation, domain.BrainToolReasoning
	}
	if output.Diagnosis != nil {
		state.AgentDiagnosis = output.Diagnosis
		state.Incident.RootCause = output.Diagnosis.Statement
		state.Incident.RootCauseCategory = output.Diagnosis.Category
		state.Incident.RootCauseVariant = output.Diagnosis.Mechanism
		state.Incident.RootCauseEvidenceIDs = append([]string(nil), output.Diagnosis.EvidenceIDs...)
		state.Incident.Confidence = output.Diagnosis.ModelConfidence
		if len(output.Diagnosis.TargetRefs) > 0 {
			target := output.Diagnosis.TargetRefs[0]
			state.Incident.RootCauseService = target.Service
			state.Incident.RootCauseResource = target.Resource
			if state.Incident.RootCauseService == "" {
				state.Incident.RootCauseService = target.Resource
			}
			if state.Incident.RootCauseResource == "" {
				state.Incident.RootCauseResource = target.Service
			}
		}
		if output.Diagnosis.Provisional {
			state.BrainPhase, state.ActiveToolCategory = domain.BrainPhaseEscalation, domain.BrainToolControl
		} else {
			state.BrainPhase, state.ActiveToolCategory = domain.BrainPhaseRecovery, domain.BrainToolRecovery
		}
	}
	if output.RecoveryPlan != nil {
		state.AgentRecoveryPlan = output.RecoveryPlan
	}
	if len(output.RequestedSkills) > 0 {
		state.RequestedSkills = output.RequestedSkills
	}
	if len(output.SkillActivations) > 0 {
		state.SkillActivations = append(state.SkillActivations, output.SkillActivations...)
	}
	if output.ReferenceContent != "" && output.ReferenceID != "" {
		if state.LoadedSkillReferences == nil {
			state.LoadedSkillReferences = map[string]string{}
		}
		state.LoadedSkillReferences[output.ReferenceID] = output.ReferenceContent
	}
	if output.SelectedCategory != "" {
		state.ActiveToolCategory = output.SelectedCategory
	}
	if output.NextPhase != "" {
		state.BrainPhase = output.NextPhase
		state.ActiveToolCategory = defaultCategoryForPhase(output.NextPhase)
	}
	if output.Termination != nil {
		state.Termination = output.Termination
	}
	switch output.Class {
	case domain.ToolResultConstraint:
		trigger := domain.ReflectionConstraintFailure
		state.PendingReflection = &trigger
	case domain.ToolResultError:
		trigger := domain.ReflectionToolFailure
		state.PendingReflection = &trigger
	case domain.ToolResultEvidence:
		if output.NewInformation {
			trigger := domain.ReflectionCriticalEvidence
			state.PendingReflection = &trigger
		}
	case domain.ToolResultValidation:
		if output.Grounding != nil && output.Grounding.Level == domain.GroundingRefuted {
			trigger := domain.ReflectionHypothesisRefuted
			state.PendingReflection = &trigger
		}
	}
	// A phase-compatible Skill request is also a complete corrective reflection:
	// it repairs the procedural blocker without inventing a BeliefDelta. Resume
	// the interrupted phase so the next context can activate that Skill and
	// expose only its bounded Tool Categories.
	reflectionResolved := output.BeliefDelta != nil || len(output.Hypotheses) > 0 || len(output.RequestedSkills) > 0
	if state.BrainPhase == domain.BrainPhaseReflection && reflectionResolved {
		if len(state.Reflections) > 0 {
			index := len(state.Reflections) - 1
			state.Reflections[index].Accepted = true
			state.Reflections[index].NextGoal = output.Summary
			if output.BeliefDelta != nil {
				state.Reflections[index].BeliefDeltas = append(state.Reflections[index].BeliefDeltas, *output.BeliefDelta)
				state.Reflections[index].HypothesisRevisionIDs = appendUnique(state.Reflections[index].HypothesisRevisionIDs, output.BeliefDelta.HypothesisRevisionID)
			}
			for _, hypothesis := range output.Hypotheses {
				state.Reflections[index].HypothesisRevisionIDs = appendUnique(state.Reflections[index].HypothesisRevisionIDs, hypothesis.ID)
			}
		}
		state.BrainPhase, state.ResumeBrainPhase, state.PendingReflection = state.ResumeBrainPhase, "", nil
		if state.PendingTermination != "" {
			termination, _ := brainruntime.NewTermination(state.PendingTermination, currentBrainTurnID(state), finalHypothesisID(state), state.EvidenceSnapshotHash, &state.ExecutionSnapshot, []string{"recovery or verification failed after the mutation boundary"}, state.BrainBudget)
			state.Termination = &termination
			state.PendingTermination = ""
		}
	}
}

func (r *brainGraphRuntime) auditRetrieval(state *WorkflowState, output brainCapabilityOutput) {
	if state == nil || state.Incident == nil || state.Incident.Investigation == nil || len(state.ToolExecutions) == 0 {
		return
	}
	execution := state.ToolExecutions[len(state.ToolExecutions)-1]
	event := domain.MemoryAccessEvent{IncidentID: state.Incident.ID, Agent: "kubepilot-brain", Scope: domain.MemoryScope{Cluster: state.Incident.Cluster, Namespace: state.Incident.Namespace}, QueryHash: brainruntime.Hash(execution.Envelope.Intent), PolicyVersion: state.ExecutionSnapshot.PolicyHash, CreatedAt: time.Now().UTC()}
	if len(output.Candidates) > 0 {
		event.Kind = domain.MemoryEpisodic
		for _, candidate := range output.Candidates {
			event.ResultIDs = append(event.ResultIDs, candidate.IncidentID)
			event.Results = append(event.Results, domain.MemoryAccessResult{ID: candidate.IncidentID, Score: candidate.Rank.FinalScore, Version: candidate.Revision})
		}
	} else {
		event.Kind = domain.MemorySemantic
		for _, pattern := range output.Patterns {
			event.ResultIDs = append(event.ResultIDs, pattern.ID)
			event.Results = append(event.Results, domain.MemoryAccessResult{ID: pattern.ID, Score: pattern.Confidence, Version: fmt.Sprintf("%d", pattern.Version)})
		}
	}
	state.Incident.Investigation.MemoryReads = append(state.Incident.Investigation.MemoryReads, event)
}

func (r *brainGraphRuntime) observationUpdate(ctx context.Context, state *WorkflowState) (*WorkflowState, error) {
	previous := state.EvidenceSnapshotHash
	if r.deps.Reasoning != nil && len(state.Incident.Evidence) > 0 {
		ranked, err := r.deps.Reasoning.RankEvidence(state.Incident, state.Incident.Evidence)
		if err == nil {
			state.RankedEvidence = ranked.RuntimeEvidence
			if len(state.RankedEvidence) == 0 {
				state.RankedEvidence = ranked.Evidence
			}
			state.Incident.Evidence = mergeEvidence(state.Incident.Evidence, state.RankedEvidence)
			state.StateAssertions = reasoning.BuildStateAssertions(state.Incident, state.RankedEvidence, state.StateAssertions, time.Now().UTC())
			state.Features = r.deps.Reasoning.BuildFeatures(state.Incident, state.RankedEvidence)
		}
	}
	// Topology is a server-owned observation derived from normalized evidence.
	// It expands only the Resource Scope validator's one-hop identities; it
	// never creates or ranks a diagnosis hypothesis.
	graph := topology.Build(state.Incident, state.Incident.Evidence)
	if len(graph.Nodes) > 0 || len(graph.Edges) > 0 {
		state.IncidentGraph = &graph
		state.Features.TopologyGraph = graph.ToDependencyGraph(state.Incident.Service)
		if r.deps.GraphStore != nil {
			if err := r.deps.GraphStore.Put(ctx, graph); err != nil {
				state.Errors = append(state.Errors, "incident topology graph persistence unavailable")
			}
		}
	}
	state.EvidenceSnapshotHash = brainruntime.EvidenceSnapshotHash(state.Incident.Evidence)
	if previous != "" && previous != state.EvidenceSnapshotHash {
		state.AgentHypotheses = brainruntime.InvalidateStaleHypotheses(state.AgentHypotheses, state.EvidenceSnapshotHash)
		if state.AgentDiagnosis != nil && state.AgentDiagnosis.EvidenceSnapshotHash != state.EvidenceSnapshotHash {
			state.AgentDiagnosis.Provisional = true
			state.AgentRecoveryPlan, state.Incident.Proposal, state.Incident.DryRun, state.DryRun = nil, nil, nil, nil
		}
	}
	r.syncInvestigation(state)
	return state, nil
}

func (r *brainGraphRuntime) beliefUpdate(_ context.Context, state *WorkflowState) (*WorkflowState, error) {
	if state.GroundingDeltaCursor < len(state.GroundingDeltas) {
		delta := state.GroundingDeltas[len(state.GroundingDeltas)-1]
		state.GroundingDeltaCursor = len(state.GroundingDeltas)
		if delta.SuggestedRevisionNeed {
			trigger := domain.ReflectionGroundingFailure
			state.PendingReflection = &trigger
		} else if delta.ConflictDetected {
			trigger := domain.ReflectionCandidateConflict
			state.PendingReflection = &trigger
		}
	}
	return state, nil
}

func (r *brainGraphRuntime) handleUnstructured(ctx context.Context, message *schema.Message) (*WorkflowState, error) {
	state, err := brainWorkflowState(ctx)
	if err != nil {
		return nil, err
	}
	state.BrainBudget.Usage.StructuredCorrections++
	now := time.Now().UTC()
	callID := "structured-guard:" + ulid.Make().String()
	result := domain.ToolResultRecord{Class: domain.ToolResultConstraint, Status: "REJECTED", Summary: "Brain turn ended without a structured tool action", ConstraintCode: "structured_action_required", OccurredAt: now, Provenance: domain.ToolResultProvenance{ToolCallID: callID, ToolName: "structured_output_guard", Collector: "brain-action-gateway", ToolSchemaHash: state.ExecutionSnapshot.ToolSchemaHash, WindowStart: now, WindowEnd: now, ObservedAt: now, RawArtifactHash: brainruntime.Hash(struct{ HasMessage bool }{message != nil}), ParserVersion: "structured-output-guard-v1"}}
	state.ToolExecutions = append(state.ToolExecutions, domain.BrainToolExecution{Result: result})
	if state.BrainBudget.Usage.StructuredCorrections >= state.BrainBudget.Limits.MaxStructuredCorrections {
		termination, _ := brainruntime.NewTermination(domain.TerminationBudgetExhausted, currentBrainTurnID(state), finalHypothesisID(state), state.EvidenceSnapshotHash, &state.ExecutionSnapshot, []string{"structured action correction budget exhausted"}, state.BrainBudget)
		state.Termination = &termination
	} else {
		trigger := domain.ReflectionConstraintFailure
		state.PendingReflection = &trigger
	}
	if message != nil {
		message.ReasoningContent = ""
	}
	r.syncInvestigation(state)
	return state, nil
}

func (r *brainGraphRuntime) terminationRouter(_ context.Context, state *WorkflowState) (*WorkflowState, error) {
	if state == nil || state.Incident == nil {
		return state, fmt.Errorf("termination router requires WorkflowState and Incident")
	}
	if state.Termination == nil && state.BrainBudget.Limits.MaxTurns > 0 && state.BrainBudget.Usage.Turns >= state.BrainBudget.Limits.MaxTurns {
		termination, _ := brainruntime.NewTermination(domain.TerminationBudgetExhausted, currentBrainTurnID(state), finalHypothesisID(state), state.EvidenceSnapshotHash, &state.ExecutionSnapshot, unresolvedGaps(state), state.BrainBudget)
		state.Termination = &termination
	}
	if state.Termination != nil && state.Incident.Investigation != nil {
		state.Incident.Investigation.CompletedAt = time.Now().UTC()
		r.syncInvestigation(state)
	}
	return state, nil
}

func (r *brainGraphRuntime) syncInvestigation(state *WorkflowState) {
	if state == nil || state.Incident == nil || state.Incident.Investigation == nil {
		return
	}
	inv := state.Incident.Investigation
	inv.BrainTurns = append([]domain.BrainTurn(nil), state.BrainTurns...)
	inv.AssistantTurns = append([]domain.AssistantTurnRecord(nil), state.AssistantTurns...)
	inv.IncidentUnderstanding = state.IncidentUnderstanding
	inv.SkillActivations = append([]domain.SkillActivation(nil), state.SkillActivations...)
	inv.ToolExecutions = append([]domain.BrainToolExecution(nil), state.ToolExecutions...)
	inv.AgentHypotheses = append([]domain.AgentHypothesis(nil), state.AgentHypotheses...)
	inv.HypothesisAdmissions = append([]domain.HypothesisAdmission(nil), state.HypothesisAdmissions...)
	inv.HypothesisGroundings = append([]domain.HypothesisGrounding(nil), state.HypothesisGroundings...)
	inv.GroundingDeltas = append([]domain.GroundingDelta(nil), state.GroundingDeltas...)
	inv.BeliefDeltas = append([]domain.BeliefDelta(nil), state.BeliefDeltas...)
	inv.Reflections = append([]domain.ReflectionRecord(nil), state.Reflections...)
	inv.AgentDiagnosis, inv.AgentRecoveryPlan, inv.Termination = state.AgentDiagnosis, state.AgentRecoveryPlan, state.Termination
	inv.BrainBudget, inv.ExecutionSnapshot = &state.BrainBudget, &state.ExecutionSnapshot
	inv.WorkflowAttempt = state.WorkflowAttempt
	inv.Signals, inv.Assertions = collectSignals(state.Incident.Evidence), append([]domain.StateAssertion(nil), state.StateAssertions...)
}

func envelopeFromToolCall(state *WorkflowState, callID, toolName string) domain.AgentActionEnvelope {
	intent := domain.AgentActionIntent{}
	for index := len(state.BrainMessages) - 1; index >= 0; index-- {
		for _, call := range state.BrainMessages[index].ToolCalls {
			if call.ID != callID {
				continue
			}
			var input struct {
				Intent              string               `json:"intent"`
				ExpectedObservation []string             `json:"expected_observation"`
				Targets             []domain.ResourceRef `json:"targets"`
				HypothesisIDs       []string             `json:"hypothesis_ids"`
				HypothesisID        string               `json:"hypothesis_id"`
				EvidenceNeed        []string             `json:"evidence_need"`
			}
			_ = json.Unmarshal([]byte(call.Function.Arguments), &input)
			intent = domain.AgentActionIntent{Intent: input.Intent, TargetScope: input.Targets, HypothesisIDs: input.HypothesisIDs, EvidenceNeed: input.EvidenceNeed, ExpectedObservation: input.ExpectedObservation}
			if input.HypothesisID != "" {
				intent.HypothesisIDs = append(intent.HypothesisIDs, input.HypothesisID)
			}
		}
	}
	return domain.AgentActionEnvelope{ActionID: callID, IncidentID: state.Incident.ID, TurnID: currentBrainTurnID(state), Phase: state.BrainPhase, ToolName: toolName, ToolCategory: categoryForToolName(toolName), SkillRefs: append([]domain.SkillRef(nil), state.ActiveSkillRefs...), EvidenceSnapshotHash: state.EvidenceSnapshotHash, IdempotencyKey: brainruntime.Hash(struct{ Incident, Call string }{state.Incident.ID, callID}), Intent: intent}
}

func categoryForToolName(name string) domain.BrainToolCategory {
	switch name {
	case "query_prometheus_evidence", "query_loki_evidence", "query_trace_evidence", "query_kubernetes_evidence":
		return domain.BrainToolEvidence
	case "retrieve_incidents", "retrieve_causal_patterns":
		return domain.BrainToolRetrieval
	case "submit_hypotheses", "revise_hypothesis", "validate_hypothesis", "commit_belief_delta", "submit_diagnosis", "submit_investigation_plan":
		return domain.BrainToolReasoning
	case "submit_recovery_plan":
		return domain.BrainToolRecovery
	default:
		return domain.BrainToolControl
	}
}

func replaceGrounding(items []domain.HypothesisGrounding, value domain.HypothesisGrounding) []domain.HypothesisGrounding {
	out := make([]domain.HypothesisGrounding, 0, len(items)+1)
	for _, item := range items {
		if item.HypothesisRevisionID != value.HypothesisRevisionID {
			out = append(out, item)
		}
	}
	return append(out, value)
}

func activeHypothesisCount(items []domain.AgentHypothesis) int {
	count := 0
	for _, item := range items {
		switch item.Status {
		case domain.HypothesisProposed, domain.HypothesisAdmitted, domain.HypothesisInvestigating, domain.HypothesisSupported:
			count++
		}
	}
	return count
}

func hypothesisBranchCount(items []domain.AgentHypothesis) int {
	lineages := map[string]bool{}
	for _, item := range items {
		lineages[item.LineageID] = true
	}
	return len(lineages)
}

func finalHypothesisID(state *WorkflowState) string {
	if state.AgentDiagnosis != nil {
		return state.AgentDiagnosis.HypothesisRevisionID
	}
	return ""
}

func unresolvedGaps(state *WorkflowState) []string {
	if len(state.HypothesisGroundings) == 0 {
		return []string{"no hypothesis has a current grounding result"}
	}
	return append([]string(nil), state.HypothesisGroundings[len(state.HypothesisGroundings)-1].MissingObservations...)
}
