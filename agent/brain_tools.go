package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/kubepilot-aiops/kubepilot/internal/brainruntime"
	"github.com/kubepilot-aiops/kubepilot/internal/domain"
	"github.com/kubepilot-aiops/kubepilot/internal/topology"
	captools "github.com/kubepilot-aiops/kubepilot/tools"
	"github.com/oklog/ulid/v2"
)

type brainStateContextKey struct{}
type brainStateRegistryContextKey struct{}

type brainStateRegistryContext struct {
	registry   *sync.Map
	incidentID string
}

func withBrainWorkflowState(ctx context.Context, state *WorkflowState) context.Context {
	return context.WithValue(ctx, brainStateContextKey{}, state)
}

func withBrainStateRegistry(ctx context.Context, registry *sync.Map, incidentID string) context.Context {
	return context.WithValue(ctx, brainStateRegistryContextKey{}, brainStateRegistryContext{registry: registry, incidentID: incidentID})
}

func brainWorkflowState(ctx context.Context) (*WorkflowState, error) {
	state, ok := ctx.Value(brainStateContextKey{}).(*WorkflowState)
	if ok && state != nil && state.Incident != nil {
		return state, nil
	}
	registered, ok := ctx.Value(brainStateRegistryContextKey{}).(brainStateRegistryContext)
	if ok && registered.registry != nil && registered.incidentID != "" {
		if value, exists := registered.registry.Load(registered.incidentID); exists {
			if state, valid := value.(*WorkflowState); valid && state != nil && state.Incident != nil {
				return state, nil
			}
		}
	}
	return nil, fmt.Errorf("brain workflow state is unavailable")
}

type brainEvidenceToolInput struct {
	Intent              string               `json:"intent" jsonschema:"required"`
	ExpectedObservation []string             `json:"expected_observation" jsonschema:"required,minItems=1"`
	Targets             []domain.ResourceRef `json:"targets" jsonschema:"required,minItems=1"`
	HypothesisIDs       []string             `json:"hypothesis_ids,omitempty"`
	EvidenceNeed        []string             `json:"evidence_need,omitempty"`
	SignalKinds         []string             `json:"signal_kinds,omitempty"`
	WindowMinutes       int                  `json:"window_minutes,omitempty"`
}

type brainRetrievalToolInput struct {
	Intent              string   `json:"intent" jsonschema:"required"`
	ExpectedObservation []string `json:"expected_observation" jsonschema:"required,minItems=1"`
	HypothesisIDs       []string `json:"hypothesis_ids,omitempty"`
	Terms               []string `json:"terms,omitempty"`
	Limit               int      `json:"limit,omitempty"`
}

type compareBrainHypothesesInput struct {
	Intent              string   `json:"intent" jsonschema:"required"`
	ExpectedObservation []string `json:"expected_observation" jsonschema:"required,minItems=1"`
	HypothesisIDs       []string `json:"hypothesis_ids" jsonschema:"required,minItems=2,maxItems=5"`
}

type validateBrainDiagnosisInput struct {
	Intent              string   `json:"intent" jsonschema:"required"`
	ExpectedObservation []string `json:"expected_observation" jsonschema:"required,minItems=1"`
	DiagnosisID         string   `json:"diagnosis_id" jsonschema:"required"`
}

type submitIncidentUnderstandingInput struct {
	Intent              string               `json:"intent" jsonschema:"required"`
	ExpectedObservation []string             `json:"expected_observation" jsonschema:"required,minItems=1"`
	Summary             string               `json:"summary" jsonschema:"required"`
	AffectedTargets     []domain.ResourceRef `json:"affected_targets" jsonschema:"required,minItems=1"`
	PossibleDomains     []string             `json:"possible_domains" jsonschema:"required,minItems=1"`
	Unknowns            []string             `json:"unknowns,omitempty"`
}

type submitInvestigationPlanInput struct {
	Intent              string   `json:"intent" jsonschema:"required"`
	ExpectedObservation []string `json:"expected_observation" jsonschema:"required,minItems=1"`
	Objective           string   `json:"objective" jsonschema:"required"`
	Goals               []string `json:"goals" jsonschema:"required,minItems=1"`
	StopConditions      []string `json:"stop_conditions" jsonschema:"required,minItems=1"`
}

type proposedBrainHypothesis struct {
	Statement               string               `json:"statement" jsonschema:"required"`
	Category                string               `json:"category" jsonschema:"required"`
	Mechanism               string               `json:"mechanism" jsonschema:"required"`
	Targets                 []domain.ResourceRef `json:"targets" jsonschema:"required,minItems=1"`
	EvidenceNeeds           []string             `json:"evidence_needs" jsonschema:"required,minItems=1"`
	FalsificationConditions []string             `json:"falsification_conditions" jsonschema:"required,minItems=1"`
	ModelConfidence         float64              `json:"model_confidence" jsonschema:"required,minimum=0,maximum=1"`
}

type submitBrainHypothesesInput struct {
	Intent              string                    `json:"intent" jsonschema:"required"`
	ExpectedObservation []string                  `json:"expected_observation" jsonschema:"required,minItems=1"`
	Hypotheses          []proposedBrainHypothesis `json:"hypotheses" jsonschema:"required,minItems=1,maxItems=5"`
}

type reviseBrainHypothesisInput struct {
	Intent              string                    `json:"intent" jsonschema:"required"`
	ExpectedObservation []string                  `json:"expected_observation" jsonschema:"required,minItems=1"`
	ParentIDs           []string                  `json:"parent_ids" jsonschema:"required,minItems=1"`
	Relation            domain.HypothesisRelation `json:"relation" jsonschema:"required,enum=REFINE,enum=REPLACE,enum=SPLIT,enum=MERGE"`
	RevisionReason      string                    `json:"revision_reason" jsonschema:"required"`
	Hypothesis          proposedBrainHypothesis   `json:"hypothesis" jsonschema:"required"`
}

type validateBrainHypothesisInput struct {
	Intent                   string   `json:"intent" jsonschema:"required"`
	ExpectedObservation      []string `json:"expected_observation" jsonschema:"required,minItems=1"`
	HypothesisID             string   `json:"hypothesis_id" jsonschema:"required"`
	SupportingEvidenceIDs    []string `json:"supporting_evidence_ids,omitempty"`
	ContradictingEvidenceIDs []string `json:"contradicting_evidence_ids,omitempty"`
	MissingObservations      []string `json:"missing_observations,omitempty"`
	ExpectedCausalNodeIDs    []string `json:"expected_causal_node_ids,omitempty"`
}

type commitBrainBeliefInput struct {
	Intent              string   `json:"intent" jsonschema:"required"`
	ExpectedObservation []string `json:"expected_observation" jsonschema:"required,minItems=1"`
	HypothesisID        string   `json:"hypothesis_id" jsonschema:"required"`
	NewConfidence       float64  `json:"new_confidence" jsonschema:"required,minimum=0,maximum=1"`
	Direction           string   `json:"direction" jsonschema:"required"`
	EvidenceIDs         []string `json:"evidence_ids,omitempty"`
	ValidationIDs       []string `json:"validation_result_ids,omitempty"`
	RevisionRequired    bool     `json:"revision_required"`
	RevisionReason      string   `json:"revision_reason,omitempty"`
}

type submitBrainDiagnosisInput struct {
	Intent              string               `json:"intent" jsonschema:"required"`
	ExpectedObservation []string             `json:"expected_observation" jsonschema:"required,minItems=1"`
	HypothesisID        string               `json:"hypothesis_id" jsonschema:"required"`
	Statement           string               `json:"statement" jsonschema:"required"`
	Category            string               `json:"category" jsonschema:"required"`
	Mechanism           string               `json:"mechanism" jsonschema:"required"`
	Targets             []domain.ResourceRef `json:"targets" jsonschema:"required,minItems=1"`
	ModelConfidence     float64              `json:"model_confidence" jsonschema:"required,minimum=0,maximum=1"`
	EvidenceIDs         []string             `json:"evidence_ids" jsonschema:"required,minItems=1"`
	ValidationResultIDs []string             `json:"validation_result_ids" jsonschema:"required,minItems=1"`
}

type requestBrainSkillsInput struct {
	Intent              string   `json:"intent" jsonschema:"required"`
	ExpectedObservation []string `json:"expected_observation" jsonschema:"required,minItems=1"`
	SkillIDs            []string `json:"skill_ids" jsonschema:"required,minItems=1,maxItems=2"`
	Reason              string   `json:"reason" jsonschema:"required"`
	Trigger             string   `json:"trigger" jsonschema:"required"`
}

type selectBrainCategoryInput struct {
	Intent              string                   `json:"intent" jsonschema:"required"`
	ExpectedObservation []string                 `json:"expected_observation" jsonschema:"required,minItems=1"`
	Category            domain.BrainToolCategory `json:"category" jsonschema:"required,enum=EVIDENCE,enum=RETRIEVAL,enum=REASONING,enum=RECOVERY,enum=CONTROL"`
	SkillIDs            []string                 `json:"skill_ids" jsonschema:"required,minItems=1,maxItems=2"`
	Reason              string                   `json:"reason" jsonschema:"required"`
	Trigger             string                   `json:"trigger" jsonschema:"required"`
}

type advanceBrainPhaseInput struct {
	Intent              string            `json:"intent" jsonschema:"required"`
	ExpectedObservation []string          `json:"expected_observation" jsonschema:"required,minItems=1"`
	NextPhase           domain.BrainPhase `json:"next_phase" jsonschema:"required,enum=DIAGNOSIS"`
}

type readBrainSkillReferenceInput struct {
	Intent              string   `json:"intent" jsonschema:"required"`
	ExpectedObservation []string `json:"expected_observation" jsonschema:"required,minItems=1"`
	SkillID             string   `json:"skill_id" jsonschema:"required"`
	Reference           string   `json:"reference" jsonschema:"required"`
}

type finishBrainInput struct {
	Intent              string                   `json:"intent" jsonschema:"required"`
	ExpectedObservation []string                 `json:"expected_observation" jsonschema:"required,minItems=1"`
	Reason              domain.TerminationReason `json:"reason" jsonschema:"required"`
	HypothesisID        string                   `json:"hypothesis_id,omitempty"`
	UnresolvedGaps      []string                 `json:"unresolved_gaps,omitempty"`
}

type submitBrainRecoveryInput struct {
	Intent              string                  `json:"intent" jsonschema:"required"`
	ExpectedObservation []string                `json:"expected_observation" jsonschema:"required,minItems=1"`
	Goal                string                  `json:"goal" jsonschema:"required"`
	PrimaryAction       domain.RecoveryOption   `json:"primary_action" jsonschema:"required"`
	Alternatives        []domain.RecoveryOption `json:"alternatives,omitempty" jsonschema:"maxItems=3"`
	ExpectedOutcome     string                  `json:"expected_outcome" jsonschema:"required"`
	RollbackPlan        string                  `json:"rollback_plan" jsonschema:"required"`
	VerificationPlan    string                  `json:"verification_plan" jsonschema:"required"`
	RiskReason          string                  `json:"risk_reason" jsonschema:"required"`
}

type brainCapabilityOutput struct {
	Class               domain.ToolResultClass        `json:"class"`
	Status              string                        `json:"status"`
	Summary             string                        `json:"summary,omitempty"`
	Provenance          domain.ToolResultProvenance   `json:"provenance"`
	NewInformation      bool                          `json:"new_information"`
	ConstraintCode      string                        `json:"constraint_code,omitempty"`
	Infrastructure      bool                          `json:"infrastructure_failure,omitempty"`
	Evidence            []domain.Evidence             `json:"evidence,omitempty"`
	EvidenceView        []domain.BrainEvidenceView    `json:"evidence_view,omitempty"`
	Resources           []domain.ResourceRef          `json:"resources,omitempty"`
	Memory              []domain.MemoryResult         `json:"memory,omitempty"`
	HistoricalIncidents []domain.RetrievalCandidate   `json:"historical_incidents,omitempty"`
	Patterns            []domain.CausalPattern        `json:"patterns,omitempty"`
	Hypotheses          []domain.AgentHypothesis      `json:"hypotheses,omitempty"`
	Admissions          []domain.HypothesisAdmission  `json:"admissions,omitempty"`
	Grounding           *domain.HypothesisGrounding   `json:"grounding,omitempty"`
	GroundingDelta      *domain.GroundingDelta        `json:"grounding_delta,omitempty"`
	Comparisons         []domain.HypothesisComparison `json:"hypothesis_comparisons,omitempty"`
	BeliefDelta         *domain.BeliefDelta           `json:"belief_delta,omitempty"`
	Diagnosis           *domain.AgentDiagnosis        `json:"diagnosis,omitempty"`
	DiagnosisValidation *domain.DiagnosisValidation   `json:"diagnosis_validation,omitempty"`
	DiagnosisFinalized  bool                          `json:"diagnosis_finalized,omitempty"`
	RecoveryPlan        *domain.AgentRecoveryPlan     `json:"recovery_plan,omitempty"`
	RequestedSkills     []SkillRequest                `json:"requested_skills,omitempty"`
	SkillActivations    []domain.SkillActivation      `json:"skill_activations,omitempty"`
	SelectedCategory    domain.BrainToolCategory      `json:"selected_category,omitempty"`
	NextPhase           domain.BrainPhase             `json:"next_phase,omitempty"`
	Termination         *domain.TerminationEvent      `json:"termination,omitempty"`
	Understanding       *domain.IncidentUnderstanding `json:"incident_understanding,omitempty"`
	InvestigationPlan   *domain.InvestigationPlan     `json:"investigation_plan,omitempty"`
	ReferenceContent    string                        `json:"reference_content,omitempty"`
	ReferenceID         string                        `json:"reference_id,omitempty"`
}

func buildBrainCapabilities(deps constrainedToolDeps, resolver *BrainSkillResolver) (*captools.Registry, error) {
	registry := captools.NewRegistry()
	capabilities := []captools.Capability{}
	for _, source := range []string{"metric", "log", "trace", "kubernetes"} {
		source := source
		name := brainEvidenceToolName(source)
		capability, err := captools.NewCapability(name, "Collect bounded, server-normalized evidence for the current Incident scope. Raw query languages are not accepted.", func(ctx context.Context, input brainEvidenceToolInput) (brainCapabilityOutput, error) {
			return runBrainEvidenceTool(ctx, deps, source, name, input)
		}, brainRegistration(captools.CategoryObservability, captools.NodeBrainEvidence))
		if err != nil {
			return nil, err
		}
		capabilities = append(capabilities, capability)
	}
	if capability, err := captools.NewCapability("discover_resources", "Resolve current-namespace resources and observed one-hop dependencies from server-owned Kubernetes topology. Free-text resource identities are never accepted.", func(ctx context.Context, input brainEvidenceToolInput) (brainCapabilityOutput, error) {
		return runBrainDiscoverResources(ctx, deps, input)
	}, brainRegistration(captools.CategoryObservability, captools.NodeBrainEvidence)); err != nil {
		return nil, err
	} else {
		capabilities = append(capabilities, capability)
	}
	if capability, err := captools.NewCapability("retrieve_incidents", "Retrieve bounded historical Incident memory as non-factual context.", func(ctx context.Context, input brainRetrievalToolInput) (brainCapabilityOutput, error) {
		return runBrainIncidentRetrieval(ctx, deps, input)
	}, brainRegistration(captools.CategoryRetrieval, captools.NodeBrainRetrieval)); err != nil {
		return nil, err
	} else {
		capabilities = append(capabilities, capability)
	}
	if capability, err := captools.NewCapability("retrieve_runbooks", "Retrieve bounded procedural memory as non-factual runbook context.", func(ctx context.Context, input brainRetrievalToolInput) (brainCapabilityOutput, error) {
		return runBrainRunbookRetrieval(ctx, deps, input)
	}, brainRegistration(captools.CategoryRetrieval, captools.NodeBrainRetrieval)); err != nil {
		return nil, err
	} else {
		capabilities = append(capabilities, capability)
	}
	if capability, err := captools.NewCapability("retrieve_patterns", "Retrieve server-maintained causal patterns as validation context, never as incident facts.", func(ctx context.Context, input brainRetrievalToolInput) (brainCapabilityOutput, error) {
		return runBrainPatternRetrieval(ctx, deps, input)
	}, brainRegistration(captools.CategoryRetrieval, captools.NodeBrainRetrieval)); err != nil {
		return nil, err
	} else {
		capabilities = append(capabilities, capability)
	}
	if capability, err := captools.NewCapability("compare_hypotheses", "Return a non-ranking comparison of existing hypothesis grounding obligations. The Runtime never selects a winner.", func(ctx context.Context, input compareBrainHypothesesInput) (brainCapabilityOutput, error) {
		return runBrainCompareHypotheses(ctx, input)
	}, brainRegistration(captools.CategoryReasoning, captools.NodeBrainReasoning)); err != nil {
		return nil, err
	} else {
		capabilities = append(capabilities, capability)
	}
	if capability, err := captools.NewCapability("submit_incident_understanding", "Persist a structured interpretation of the Incident without asserting a root cause.", func(ctx context.Context, input submitIncidentUnderstandingInput) (brainCapabilityOutput, error) {
		return runBrainSubmitUnderstanding(ctx, input)
	}, brainRegistration(captools.CategoryAgent, controlNodesForBrain()...)); err != nil {
		return nil, err
	} else {
		capabilities = append(capabilities, capability)
	}
	if capability, err := captools.NewCapability("validate_diagnosis", "Validate the immutable LLM diagnosis references and snapshots without changing its semantics, selection, or confidence.", func(ctx context.Context, input validateBrainDiagnosisInput) (brainCapabilityOutput, error) {
		return runBrainValidateDiagnosis(ctx, input)
	}, brainRegistration(captools.CategoryReasoning, captools.NodeBrainReasoning)); err != nil {
		return nil, err
	} else {
		capabilities = append(capabilities, capability)
	}
	if capability, err := captools.NewCapability("submit_investigation_plan", "Persist the Brain's investigation objective, goals, and stop conditions.", func(ctx context.Context, input submitInvestigationPlanInput) (brainCapabilityOutput, error) {
		return runBrainSubmitPlan(ctx, input)
	}, brainRegistration(captools.CategoryReasoning, captools.NodeBrainReasoning)); err != nil {
		return nil, err
	} else {
		capabilities = append(capabilities, capability)
	}
	if capability, err := captools.NewCapability("submit_hypotheses", "Submit open-world diagnosis hypotheses for structural, scope, permission, and verifiability admission only.", func(ctx context.Context, input submitBrainHypothesesInput) (brainCapabilityOutput, error) {
		return runBrainSubmitHypotheses(ctx, deps, input)
	}, brainRegistration(captools.CategoryReasoning, captools.NodeBrainReasoning)); err != nil {
		return nil, err
	} else {
		capabilities = append(capabilities, capability)
	}
	if capability, err := captools.NewCapability("revise_hypothesis", "Create an immutable hypothesis revision with explicit lineage and revision reason.", func(ctx context.Context, input reviseBrainHypothesisInput) (brainCapabilityOutput, error) {
		return runBrainReviseHypothesis(ctx, deps, input)
	}, brainRegistration(captools.CategoryReasoning, captools.NodeBrainReasoning)); err != nil {
		return nil, err
	} else {
		capabilities = append(capabilities, capability)
	}
	if capability, err := captools.NewCapability("validate_hypothesis", "Validate a hypothesis only against cited server evidence IDs, explicit coverage obligations, and server causal node IDs.", func(ctx context.Context, input validateBrainHypothesisInput) (brainCapabilityOutput, error) {
		return runBrainValidateHypothesis(ctx, input)
	}, brainRegistration(captools.CategoryReasoning, captools.NodeBrainReasoning)); err != nil {
		return nil, err
	} else {
		capabilities = append(capabilities, capability)
	}
	if capability, err := captools.NewCapability("commit_belief_delta", "Commit the model's subjective confidence update; semantic changes require a new hypothesis revision.", func(ctx context.Context, input commitBrainBeliefInput) (brainCapabilityOutput, error) {
		return runBrainCommitBelief(ctx, input)
	}, brainRegistration(captools.CategoryReasoning, captools.NodeBrainReasoning)); err != nil {
		return nil, err
	} else {
		capabilities = append(capabilities, capability)
	}
	if capability, err := captools.NewCapability("submit_diagnosis", "Persist the LLM-selected diagnosis without changing its statement, mechanism, target, or confidence.", func(ctx context.Context, input submitBrainDiagnosisInput) (brainCapabilityOutput, error) {
		return runBrainSubmitDiagnosis(ctx, input)
	}, brainRegistration(captools.CategoryReasoning, captools.NodeBrainReasoning)); err != nil {
		return nil, err
	} else {
		capabilities = append(capabilities, capability)
	}
	if capability, err := captools.NewCapability("submit_recovery_plan", "Submit a recovery plan for Safety Kernel validation; this tool cannot mutate Kubernetes.", func(ctx context.Context, input submitBrainRecoveryInput) (brainCapabilityOutput, error) {
		return runBrainSubmitRecovery(ctx, input)
	}, brainRegistration(captools.CategoryDecision, captools.NodeBrainRecovery)); err != nil {
		return nil, err
	} else {
		capabilities = append(capabilities, capability)
	}
	controlNodes := controlNodesForBrain()
	if capability, err := captools.NewCapability("request_skills", "Request optional versioned Skills for the next Brain turn.", func(ctx context.Context, input requestBrainSkillsInput) (brainCapabilityOutput, error) {
		return runBrainRequestSkills(ctx, resolver, input)
	}, brainRegistration(captools.CategoryAgent, controlNodes...)); err != nil {
		return nil, err
	} else {
		capabilities = append(capabilities, capability)
	}
	if capability, err := captools.NewCapability("read_skill_reference", "Load one declared reference from an active versioned Skill.", func(ctx context.Context, input readBrainSkillReferenceInput) (brainCapabilityOutput, error) {
		return runBrainReadSkillReference(ctx, resolver, input)
	}, brainRegistration(captools.CategoryAgent, controlNodes...)); err != nil {
		return nil, err
	} else {
		capabilities = append(capabilities, capability)
	}
	if capability, err := captools.NewCapability("select_tool_category", "Atomically request exact optional Skill IDs and select the single primary Tool Category they grant for the next Brain turn.", func(ctx context.Context, input selectBrainCategoryInput) (brainCapabilityOutput, error) {
		return runBrainSelectCategory(ctx, resolver, input)
	}, brainRegistration(captools.CategoryAgent, controlNodes...)); err != nil {
		return nil, err
	} else {
		capabilities = append(capabilities, capability)
	}
	if capability, err := captools.NewCapability("advance_brain_phase", "Request a server-validated transition from investigation to diagnosis.", func(ctx context.Context, input advanceBrainPhaseInput) (brainCapabilityOutput, error) {
		return runBrainAdvancePhase(ctx, input)
	}, brainRegistration(captools.CategoryAgent, controlNodes...)); err != nil {
		return nil, err
	} else {
		capabilities = append(capabilities, capability)
	}
	if capability, err := captools.NewCapability("finish_investigation", "Terminate or escalate the Brain loop with one explicit audited reason.", func(ctx context.Context, input finishBrainInput) (brainCapabilityOutput, error) {
		return runBrainFinish(ctx, input)
	}, brainRegistration(captools.CategoryAgent, controlNodes...)); err != nil {
		return nil, err
	} else {
		capabilities = append(capabilities, capability)
	}
	if err := registry.RegisterAll(context.Background(), capabilities...); err != nil {
		return nil, err
	}
	return registry, nil
}

func brainEvidenceToolName(source string) string {
	switch source {
	case "metric":
		return "query_metrics"
	case "log":
		return "search_logs"
	case "trace":
		return "query_traces"
	default:
		return "inspect_kubernetes"
	}
}

func controlNodesForBrain() []string {
	return []string{captools.NodeBrainEvidence, captools.NodeBrainRetrieval, captools.NodeBrainReasoning, captools.NodeBrainRecovery, captools.NodeBrainControl}
}

func brainRegistration(category captools.ToolCategory, nodes ...string) captools.Registration {
	return captools.Registration{Category: category, AllowedNodes: nodes, Timeout: 90 * time.Second, MaxArgumentBytes: 128 << 10, MaxOutputBytes: 256 << 10}
}

func runBrainEvidenceTool(ctx context.Context, deps constrainedToolDeps, source, toolName string, input brainEvidenceToolInput) (brainCapabilityOutput, error) {
	state, err := brainWorkflowState(ctx)
	if err != nil {
		return brainCapabilityOutput{}, err
	}
	envelope := newBrainEnvelope(state, toolName, domain.BrainToolEvidence, domain.AgentActionIntent{Intent: input.Intent, TargetScope: input.Targets, HypothesisIDs: input.HypothesisIDs, EvidenceNeed: input.EvidenceNeed, ExpectedObservation: input.ExpectedObservation})
	if denied := authorizeBrainTool(state, envelope); denied != nil {
		return *denied, nil
	}
	for _, target := range input.Targets {
		admission := (brainruntime.AdmissionService{}).Admit(domain.AgentHypothesis{ID: "scope-check", Statement: "scope check", Mechanism: "scope check", TargetRefs: []domain.ResourceRef{target}, EvidenceNeeds: []string{"scope"}, FalsificationConditions: []string{"scope invalid"}}, brainruntime.AdmissionContext{Incident: state.Incident, Graph: state.IncidentGraph, ExternalInventory: deps.ExternalInventory, AvailableToolCategories: []domain.BrainToolCategory{domain.BrainToolEvidence}})
		if admission.Decision != "ADMITTED" {
			return constraintBrainOutput(envelope, "target_scope_denied", "requested target is outside the Incident scope"), nil
		}
	}
	collector := deps.Collectors[source]
	if collector == nil && source == "kubernetes" {
		collector = deps.Collectors["topology"]
	}
	if collector == nil {
		return errorBrainOutput(envelope, source+" collector unavailable", true), nil
	}
	incident := *state.Incident
	window := input.WindowMinutes
	if window <= 0 || window > 15 {
		window = 5
	}
	end := time.Now().UTC()
	start := end.Add(-time.Duration(window) * time.Minute)
	incident.EvidenceStartAt = start
	request := domain.EvidenceRequest{Source: source, Targets: append([]domain.ResourceRef(nil), input.Targets...), SignalKinds: append([]string(nil), input.SignalKinds...), WindowStart: start, WindowEnd: end, HypothesisIDs: append([]string(nil), input.HypothesisIDs...)}
	items, collectErr := collector.Collect(ctx, &incident, request)
	if collectErr != nil {
		return errorBrainOutput(envelope, source+" collector failed", true), nil
	}
	items = normalizeCollectedEvidence(state.Incident, items)
	ids := evidenceIDs(items)
	class, status := domain.ToolResultEvidence, "OK"
	if len(items) == 0 {
		// An empty collector response is an observed validation outcome, not an
		// Evidence record with a missing ID. This keeps provenance complete and
		// allows the no-information policy to stop repeated collection.
		class, status = domain.ToolResultValidation, "NO_EVIDENCE"
	}
	return brainCapabilityOutput{Class: class, Status: status, Summary: fmt.Sprintf("%s returned %d normalized evidence records", toolName, len(items)), NewInformation: hasNewEvidence(state.Incident.Evidence, items), Evidence: items, Provenance: domain.ToolResultProvenance{ToolName: toolName, Collector: source, TargetRefs: append([]domain.ResourceRef(nil), input.Targets...), WindowStart: start, WindowEnd: end, ObservedAt: end, RawArtifactHash: brainruntime.Hash(items), ParserVersion: "brain-evidence-v1", EvidenceIDs: ids}}, nil
}

func runBrainDiscoverResources(ctx context.Context, deps constrainedToolDeps, input brainEvidenceToolInput) (brainCapabilityOutput, error) {
	output, err := runBrainEvidenceTool(ctx, deps, "kubernetes", "discover_resources", input)
	if err != nil || output.Class == domain.ToolResultConstraint || output.Class == domain.ToolResultError {
		return output, err
	}
	state, stateErr := brainWorkflowState(ctx)
	if stateErr != nil {
		return brainCapabilityOutput{}, stateErr
	}
	graph := topology.Build(state.Incident, mergeEvidence(state.Incident.Evidence, output.Evidence))
	output.Resources = resourceRefsFromGraph(state.Incident, graph)
	output.NewInformation = output.NewInformation || len(output.Resources) > 0
	output.Summary = fmt.Sprintf("discover_resources returned %d server-resolved resources and %d normalized evidence records", len(output.Resources), len(output.Evidence))
	output.Provenance.RawArtifactHash = brainruntime.Hash(struct {
		Resources []domain.ResourceRef `json:"resources"`
		Evidence  []domain.Evidence    `json:"evidence"`
	}{output.Resources, output.Evidence})
	return output, nil
}

func resourceRefsFromGraph(incident *domain.Incident, graph topology.IncidentGraph) []domain.ResourceRef {
	if incident == nil {
		return nil
	}
	refs := make([]domain.ResourceRef, 0, len(graph.Nodes))
	seen := map[string]bool{}
	for _, node := range graph.Nodes {
		if strings.TrimSpace(node.ID) == "" {
			continue
		}
		kind := strings.TrimSpace(node.Type)
		if kind == "" {
			kind = "Service"
		} else {
			kind = strings.ToUpper(kind[:1]) + kind[1:]
		}
		ref := domain.ResourceRef{Namespace: incident.Namespace, Kind: kind, Resource: node.ID}
		switch strings.ToLower(node.Type) {
		case "service", "database", "cache", "queue":
			ref.Service = node.ID
		}
		if value := strings.TrimSpace(node.Metadata["service"]); value != "" {
			ref.Service = value
		}
		if value := strings.TrimSpace(node.Metadata["resource"]); value != "" {
			ref.Resource = value
		}
		key := ref.Namespace + "\x00" + ref.Kind + "\x00" + ref.Service + "\x00" + ref.Resource
		if !seen[key] {
			seen[key] = true
			refs = append(refs, ref)
		}
	}
	sort.Slice(refs, func(i, j int) bool {
		left := refs[i].Namespace + "\x00" + refs[i].Kind + "\x00" + refs[i].Service + "\x00" + refs[i].Resource
		right := refs[j].Namespace + "\x00" + refs[j].Kind + "\x00" + refs[j].Service + "\x00" + refs[j].Resource
		return left < right
	})
	return refs
}

func runBrainIncidentRetrieval(ctx context.Context, deps constrainedToolDeps, input brainRetrievalToolInput) (brainCapabilityOutput, error) {
	state, err := brainWorkflowState(ctx)
	if err != nil {
		return brainCapabilityOutput{}, err
	}
	envelope := newBrainEnvelope(state, "retrieve_incidents", domain.BrainToolRetrieval, domain.AgentActionIntent{Intent: input.Intent, HypothesisIDs: input.HypothesisIDs, ExpectedObservation: input.ExpectedObservation})
	if denied := authorizeBrainTool(state, envelope); denied != nil {
		return *denied, nil
	}
	if deps.Historical == nil {
		return errorBrainOutput(envelope, "historical retrieval unavailable", true), nil
	}
	limit := input.Limit
	if limit <= 0 || limit > 10 {
		limit = 5
	}
	features := state.Features
	if deps.Reasoning != nil && len(features.Terms) == 0 {
		features = deps.Reasoning.BuildFeatures(state.Incident, state.Incident.Evidence)
	}
	semantic, _ := deps.Historical.Semantic(ctx, features, limit)
	lexical, _ := deps.Historical.Lexical(ctx, features, limit)
	candidates := append(semantic, lexical...)
	if len(candidates) > limit {
		candidates = candidates[:limit]
	}
	return brainCapabilityOutput{Class: domain.ToolResultValidation, Status: "OK", Summary: "historical context retrieved; it is not current incident evidence", HistoricalIncidents: candidates, NewInformation: len(candidates) > 0, Provenance: baseBrainProvenance(envelope, "historical-memory", candidates)}, nil
}

func runBrainRunbookRetrieval(ctx context.Context, deps constrainedToolDeps, input brainRetrievalToolInput) (brainCapabilityOutput, error) {
	state, err := brainWorkflowState(ctx)
	if err != nil {
		return brainCapabilityOutput{}, err
	}
	envelope := newBrainEnvelope(state, "retrieve_runbooks", domain.BrainToolRetrieval, domain.AgentActionIntent{Intent: input.Intent, HypothesisIDs: input.HypothesisIDs, ExpectedObservation: input.ExpectedObservation})
	if denied := authorizeBrainTool(state, envelope); denied != nil {
		return *denied, nil
	}
	if deps.Memory == nil {
		return errorBrainOutput(envelope, "runbook retrieval unavailable", true), nil
	}
	limit := input.Limit
	if limit <= 0 || limit > 10 {
		limit = 5
	}
	terms := uniqueBrainValues(input.Terms)
	if len(terms) == 0 {
		terms = uniqueBrainValues([]string{state.Incident.Service, state.Incident.Resource})
	}
	items, readErr := deps.Memory.Read(ctx, domain.MemoryQuery{IncidentID: state.Incident.ID, Agent: "kubepilot-brain", Kind: domain.MemoryProcedural, Scope: domain.MemoryScope{Cluster: state.Incident.Cluster, Namespace: state.Incident.Namespace}, Terms: terms, Limit: limit})
	if readErr != nil {
		return errorBrainOutput(envelope, "runbook retrieval failed", true), nil
	}
	status := "OK"
	if len(items) == 0 {
		status = "NO_RESULTS"
	}
	return brainCapabilityOutput{Class: domain.ToolResultValidation, Status: status, Summary: "procedural memory retrieved as non-factual context", Memory: items, NewInformation: len(items) > 0, Provenance: baseBrainProvenance(envelope, "procedural-memory", items)}, nil
}

func runBrainPatternRetrieval(ctx context.Context, deps constrainedToolDeps, input brainRetrievalToolInput) (brainCapabilityOutput, error) {
	state, err := brainWorkflowState(ctx)
	if err != nil {
		return brainCapabilityOutput{}, err
	}
	envelope := newBrainEnvelope(state, "retrieve_patterns", domain.BrainToolRetrieval, domain.AgentActionIntent{Intent: input.Intent, HypothesisIDs: input.HypothesisIDs, ExpectedObservation: input.ExpectedObservation})
	if denied := authorizeBrainTool(state, envelope); denied != nil {
		return *denied, nil
	}
	if deps.Knowledge == nil {
		return errorBrainOutput(envelope, "causal pattern retrieval unavailable", true), nil
	}
	patterns, loadErr := deps.Knowledge.ListCausalPatterns(ctx, "active")
	if loadErr != nil {
		return errorBrainOutput(envelope, "causal pattern retrieval failed", true), nil
	}
	patterns = causalPatternsForScope(patterns, state.Incident.Cluster, state.Incident.Namespace, input.Limit)
	return brainCapabilityOutput{Class: domain.ToolResultValidation, Status: "OK", Summary: "causal validation context retrieved; it is not current incident evidence", Patterns: patterns, NewInformation: len(patterns) > 0, Provenance: baseBrainProvenance(envelope, "causal-pattern-store", patterns)}, nil
}

func runBrainSubmitHypotheses(ctx context.Context, deps constrainedToolDeps, input submitBrainHypothesesInput) (brainCapabilityOutput, error) {
	state, err := brainWorkflowState(ctx)
	if err != nil {
		return brainCapabilityOutput{}, err
	}
	if state.BrainPhase != domain.BrainPhaseInvestigation && state.BrainPhase != domain.BrainPhaseReflection {
		return phaseConstraintOutput(state, "submit_hypotheses", domain.BrainToolReasoning, input.Intent, domain.BrainPhaseInvestigation), nil
	}
	envelope := newBrainEnvelope(state, "submit_hypotheses", domain.BrainToolReasoning, domain.AgentActionIntent{Intent: input.Intent, ExpectedObservation: input.ExpectedObservation})
	if denied := authorizeBrainTool(state, envelope); denied != nil {
		return *denied, nil
	}
	if state.BrainBudget.Usage.ActiveHypotheses+len(input.Hypotheses) > state.BrainBudget.Limits.MaxActiveHypotheses {
		return constraintBrainOutput(envelope, "active_hypothesis_budget", "active hypothesis budget exceeded"), nil
	}
	now := time.Now().UTC()
	hypotheses := make([]domain.AgentHypothesis, 0, len(input.Hypotheses))
	admissions := make([]domain.HypothesisAdmission, 0, len(input.Hypotheses))
	available := availableBrainCategories(state)
	for _, proposal := range input.Hypotheses {
		id := "hyp:" + ulid.Make().String()
		hypothesis := domain.AgentHypothesis{ID: id, LineageID: id, Version: 1, Relation: domain.HypothesisRoot, Statement: proposal.Statement, Category: proposal.Category, Mechanism: proposal.Mechanism, TargetRefs: proposal.Targets, EvidenceNeeds: proposal.EvidenceNeeds, FalsificationConditions: proposal.FalsificationConditions, ModelConfidence: proposal.ModelConfidence, Status: domain.HypothesisProposed, CreatedByTurn: currentBrainTurnID(state), CreatedAt: now}
		admission := (brainruntime.AdmissionService{}).Admit(hypothesis, brainruntime.AdmissionContext{Incident: state.Incident, Graph: state.IncidentGraph, ExternalInventory: deps.ExternalInventory, AvailableToolCategories: available})
		if admission.Decision == "ADMITTED" {
			hypothesis.Status = domain.HypothesisAdmitted
		}
		hypotheses = append(hypotheses, hypothesis)
		admissions = append(admissions, admission)
	}
	return brainCapabilityOutput{Class: domain.ToolResultValidation, Status: "OK", Summary: "hypotheses received; admission checked structure, validation path, scope, permission, and safety only", Hypotheses: hypotheses, Admissions: admissions, NewInformation: len(hypotheses) > 0, Provenance: baseBrainProvenance(envelope, "hypothesis-admission-v1", hypotheses)}, nil
}

func runBrainSubmitUnderstanding(ctx context.Context, input submitIncidentUnderstandingInput) (brainCapabilityOutput, error) {
	state, err := brainWorkflowState(ctx)
	if err != nil {
		return brainCapabilityOutput{}, err
	}
	if state.BrainPhase != domain.BrainPhaseIntake {
		return phaseConstraintOutput(state, "submit_incident_understanding", domain.BrainToolControl, input.Intent, domain.BrainPhaseIntake), nil
	}
	envelope := newBrainEnvelope(state, "submit_incident_understanding", domain.BrainToolControl, domain.AgentActionIntent{Intent: input.Intent, TargetScope: input.AffectedTargets, ExpectedObservation: input.ExpectedObservation})
	if denied := authorizeBrainTool(state, envelope); denied != nil {
		return *denied, nil
	}
	understanding := domain.IncidentUnderstanding{Summary: input.Summary, AffectedTargets: input.AffectedTargets, PossibleDomains: input.PossibleDomains, Unknowns: input.Unknowns, SubmittedAt: time.Now().UTC()}
	return brainCapabilityOutput{Class: domain.ToolResultValidation, Status: "OK", Summary: "Incident understanding persisted without root-cause authority", Understanding: &understanding, NewInformation: true, Provenance: baseBrainProvenance(envelope, "incident-understanding-v1", understanding)}, nil
}

func runBrainSubmitPlan(ctx context.Context, input submitInvestigationPlanInput) (brainCapabilityOutput, error) {
	state, err := brainWorkflowState(ctx)
	if err != nil {
		return brainCapabilityOutput{}, err
	}
	if state.BrainPhase != domain.BrainPhasePlanning {
		return phaseConstraintOutput(state, "submit_investigation_plan", domain.BrainToolReasoning, input.Intent, domain.BrainPhasePlanning), nil
	}
	envelope := newBrainEnvelope(state, "submit_investigation_plan", domain.BrainToolReasoning, domain.AgentActionIntent{Intent: input.Intent, ExpectedObservation: input.ExpectedObservation})
	if denied := authorizeBrainTool(state, envelope); denied != nil {
		return *denied, nil
	}
	plan := domain.InvestigationPlan{Objective: input.Objective, StopConditions: input.StopConditions, RoundLimit: state.BrainBudget.Limits.MaxTurns, CreatedAt: time.Now().UTC()}
	for index, goal := range input.Goals {
		plan.Tasks = append(plan.Tasks, domain.WorkerTask{ID: fmt.Sprintf("brain-goal-%d", index+1), Question: goal})
	}
	return brainCapabilityOutput{Class: domain.ToolResultValidation, Status: "OK", Summary: "investigation plan persisted", InvestigationPlan: &plan, NewInformation: true, Provenance: baseBrainProvenance(envelope, "investigation-plan-v1", plan)}, nil
}

func runBrainReviseHypothesis(ctx context.Context, deps constrainedToolDeps, input reviseBrainHypothesisInput) (brainCapabilityOutput, error) {
	state, err := brainWorkflowState(ctx)
	if err != nil {
		return brainCapabilityOutput{}, err
	}
	if state.BrainPhase != domain.BrainPhaseInvestigation && state.BrainPhase != domain.BrainPhaseReflection {
		return phaseConstraintOutput(state, "revise_hypothesis", domain.BrainToolReasoning, input.Intent, domain.BrainPhaseInvestigation), nil
	}
	envelope := newBrainEnvelope(state, "revise_hypothesis", domain.BrainToolReasoning, domain.AgentActionIntent{Intent: input.Intent, HypothesisIDs: input.ParentIDs, ExpectedObservation: input.ExpectedObservation})
	if denied := authorizeBrainTool(state, envelope); denied != nil {
		return *denied, nil
	}
	parents := []domain.AgentHypothesis{}
	for _, id := range input.ParentIDs {
		parent, ok := findAgentHypothesis(state.AgentHypotheses, id)
		if !ok {
			return constraintBrainOutput(envelope, "unknown_parent_revision", "hypothesis parent revision does not exist"), nil
		}
		parents = append(parents, parent)
	}
	lineage := parents[0].LineageID
	version := 1
	for _, existing := range state.AgentHypotheses {
		if existing.LineageID == lineage && existing.Version >= version {
			version = existing.Version + 1
		}
	}
	if version > state.BrainBudget.Limits.MaxRevisionsPerLineage+1 {
		return constraintBrainOutput(envelope, "lineage_revision_budget", "hypothesis lineage revision budget exceeded"), nil
	}
	id := "hyp:" + ulid.Make().String()
	hypothesis := domain.AgentHypothesis{ID: id, LineageID: lineage, Version: version, ParentIDs: append([]string(nil), input.ParentIDs...), Relation: input.Relation, RevisionReason: input.RevisionReason, Statement: input.Hypothesis.Statement, Category: input.Hypothesis.Category, Mechanism: input.Hypothesis.Mechanism, TargetRefs: input.Hypothesis.Targets, EvidenceNeeds: input.Hypothesis.EvidenceNeeds, FalsificationConditions: input.Hypothesis.FalsificationConditions, ModelConfidence: input.Hypothesis.ModelConfidence, Status: domain.HypothesisProposed, CreatedByTurn: currentBrainTurnID(state), CreatedAt: time.Now().UTC()}
	admission := (brainruntime.AdmissionService{}).Admit(hypothesis, brainruntime.AdmissionContext{Incident: state.Incident, Graph: state.IncidentGraph, ExternalInventory: deps.ExternalInventory, AvailableToolCategories: availableBrainCategories(state)})
	if admission.Decision == "ADMITTED" {
		hypothesis.Status = domain.HypothesisAdmitted
	}
	return brainCapabilityOutput{Class: domain.ToolResultValidation, Status: "OK", Summary: "immutable hypothesis revision created", Hypotheses: []domain.AgentHypothesis{hypothesis}, Admissions: []domain.HypothesisAdmission{admission}, NewInformation: true, Provenance: baseBrainProvenance(envelope, "hypothesis-lineage-v1", hypothesis)}, nil
}

func runBrainValidateHypothesis(ctx context.Context, input validateBrainHypothesisInput) (brainCapabilityOutput, error) {
	state, err := brainWorkflowState(ctx)
	if err != nil {
		return brainCapabilityOutput{}, err
	}
	if state.BrainPhase != domain.BrainPhaseInvestigation && state.BrainPhase != domain.BrainPhaseReflection {
		return phaseConstraintOutput(state, "validate_hypothesis", domain.BrainToolReasoning, input.Intent, domain.BrainPhaseInvestigation), nil
	}
	envelope := newBrainEnvelope(state, "validate_hypothesis", domain.BrainToolReasoning, domain.AgentActionIntent{Intent: input.Intent, HypothesisIDs: []string{input.HypothesisID}, ExpectedObservation: input.ExpectedObservation})
	if denied := authorizeBrainTool(state, envelope); denied != nil {
		return *denied, nil
	}
	hypothesis, ok := findAgentHypothesis(state.AgentHypotheses, input.HypothesisID)
	if !ok {
		return constraintBrainOutput(envelope, "unknown_hypothesis_revision", "hypothesis revision does not exist"), nil
	}
	admission, ok := findHypothesisAdmission(state.HypothesisAdmissions, hypothesis.ID)
	if !ok || admission.Decision != "ADMITTED" {
		return constraintBrainOutput(envelope, "hypothesis_not_admitted", "hypothesis is not admitted for validation"), nil
	}
	var previous *domain.HypothesisGrounding
	if value, found := findHypothesisGrounding(state.HypothesisGroundings, hypothesis.ID); found {
		previous = &value
	}
	fulfilledNeeds := serverFulfilledEvidenceNeeds(state, hypothesis, input.SupportingEvidenceIDs)
	causalNodes := serverValidatedCausalNodes(state.Incident.Evidence, input.SupportingEvidenceIDs, input.ExpectedCausalNodeIDs)
	grounding, delta := (brainruntime.Grounder{}).Validate(hypothesis, state.Incident.Evidence, brainruntime.ValidationInput{SupportingEvidenceIDs: input.SupportingEvidenceIDs, ContradictingEvidenceIDs: input.ContradictingEvidenceIDs, FulfilledEvidenceNeeds: fulfilledNeeds, MissingObservations: input.MissingObservations, ExpectedCausalNodeIDs: causalNodes, CausalClaim: strings.TrimSpace(hypothesis.Mechanism) != "", TargetScopeDecisions: admission.ResourceScope, WindowStart: state.Incident.EvidenceStartAt, WindowEnd: time.Now().UTC()}, previous)
	return brainCapabilityOutput{Class: domain.ToolResultValidation, Status: "OK", Summary: "server grounding calculated from cited IDs and explicit coverage obligations", Grounding: &grounding, GroundingDelta: &delta, NewInformation: len(delta.EvidenceChange) > 0 || previous == nil || previous.Level != grounding.Level, Provenance: baseBrainProvenance(envelope, "hypothesis-grounding-v1", grounding)}, nil
}

func runBrainCompareHypotheses(ctx context.Context, input compareBrainHypothesesInput) (brainCapabilityOutput, error) {
	state, err := brainWorkflowState(ctx)
	if err != nil {
		return brainCapabilityOutput{}, err
	}
	if state.BrainPhase != domain.BrainPhaseInvestigation && state.BrainPhase != domain.BrainPhaseReflection {
		return phaseConstraintOutput(state, "compare_hypotheses", domain.BrainToolReasoning, input.Intent, domain.BrainPhaseInvestigation), nil
	}
	ids := uniqueBrainValues(input.HypothesisIDs)
	envelope := newBrainEnvelope(state, "compare_hypotheses", domain.BrainToolReasoning, domain.AgentActionIntent{Intent: input.Intent, HypothesisIDs: ids, ExpectedObservation: input.ExpectedObservation})
	if denied := authorizeBrainTool(state, envelope); denied != nil {
		return *denied, nil
	}
	if len(ids) < 2 {
		return constraintBrainOutput(envelope, "comparison_requires_competitors", "at least two distinct hypothesis revisions are required"), nil
	}
	comparisons := make([]domain.HypothesisComparison, 0, len(ids))
	for _, id := range ids {
		if _, ok := findAgentHypothesis(state.AgentHypotheses, id); !ok {
			return constraintBrainOutput(envelope, "unknown_hypothesis_revision", "comparison references an unknown hypothesis revision"), nil
		}
		grounding, ok := findHypothesisGrounding(state.HypothesisGroundings, id)
		if !ok {
			return constraintBrainOutput(envelope, "comparison_requires_validation", "every compared hypothesis must have a validation result"), nil
		}
		comparisons = append(comparisons, domain.HypothesisComparison{HypothesisRevisionID: id, Level: grounding.Level, Evidence: grounding.Evidence, Coverage: grounding.Coverage, MissingObservations: append([]string(nil), grounding.MissingObservations...), EvidenceSnapshotHash: grounding.EvidenceSnapshotHash})
	}
	return brainCapabilityOutput{Class: domain.ToolResultValidation, Status: "OK", Summary: "grounding obligations compared without ranking or selecting a diagnosis", Comparisons: comparisons, NewInformation: true, Provenance: baseBrainProvenance(envelope, "hypothesis-comparison-v1", comparisons)}, nil
}

func serverFulfilledEvidenceNeeds(state *WorkflowState, hypothesis domain.AgentHypothesis, supportingIDs []string) []string {
	supporting := map[string]bool{}
	for _, id := range supportingIDs {
		supporting[id] = true
	}
	wanted := map[string]string{}
	for _, need := range hypothesis.EvidenceNeeds {
		wanted[strings.ToLower(strings.TrimSpace(need))] = need
	}
	fulfilled := []string{}
	for _, execution := range state.ToolExecutions {
		if execution.Result.Class != domain.ToolResultEvidence || !brainContainsString(execution.Envelope.Intent.HypothesisIDs, hypothesis.ID) {
			continue
		}
		boundSupport := false
		for _, id := range execution.Result.Provenance.EvidenceIDs {
			if supporting[id] {
				boundSupport = true
				break
			}
		}
		if !boundSupport {
			continue
		}
		for _, declared := range execution.Envelope.Intent.EvidenceNeed {
			if original, ok := wanted[strings.ToLower(strings.TrimSpace(declared))]; ok {
				fulfilled = append(fulfilled, original)
			}
		}
	}
	return uniqueBrainValues(fulfilled)
}

func serverValidatedCausalNodes(evidence []domain.Evidence, supportingIDs, requested []string) []string {
	supporting := map[string]bool{}
	for _, id := range supportingIDs {
		supporting[id] = true
	}
	observed := map[string]bool{}
	for _, item := range evidence {
		if !supporting[item.ID] {
			continue
		}
		for _, nodeID := range item.CausalNodeIDs {
			observed[nodeID] = true
		}
	}
	validated := []string{}
	for _, nodeID := range requested {
		if observed[nodeID] {
			validated = append(validated, nodeID)
		}
	}
	return uniqueBrainValues(validated)
}

func brainContainsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func uniqueBrainValues(values []string) []string {
	result := []string{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" && !brainContainsString(result, value) {
			result = append(result, value)
		}
	}
	sort.Strings(result)
	return result
}

func runBrainCommitBelief(ctx context.Context, input commitBrainBeliefInput) (brainCapabilityOutput, error) {
	state, err := brainWorkflowState(ctx)
	if err != nil {
		return brainCapabilityOutput{}, err
	}
	if state.BrainPhase != domain.BrainPhaseReflection {
		return phaseConstraintOutput(state, "commit_belief_delta", domain.BrainToolReasoning, input.Intent, domain.BrainPhaseReflection), nil
	}
	envelope := newBrainEnvelope(state, "commit_belief_delta", domain.BrainToolReasoning, domain.AgentActionIntent{Intent: input.Intent, HypothesisIDs: []string{input.HypothesisID}, ExpectedObservation: input.ExpectedObservation})
	if denied := authorizeBrainTool(state, envelope); denied != nil {
		return *denied, nil
	}
	hypothesis, ok := findAgentHypothesis(state.AgentHypotheses, input.HypothesisID)
	if !ok {
		return constraintBrainOutput(envelope, "unknown_hypothesis_revision", "hypothesis revision does not exist"), nil
	}
	delta := domain.BeliefDelta{HypothesisRevisionID: input.HypothesisID, PreviousConfidence: hypothesis.ModelConfidence, NewConfidence: input.NewConfidence, Direction: input.Direction, EvidenceIDs: input.EvidenceIDs, ValidationResultIDs: input.ValidationIDs, RevisionRequired: input.RevisionRequired, RevisionReason: input.RevisionReason, OccurredAt: time.Now().UTC()}
	if _, commitErr := brainruntime.CommitBelief(hypothesis, delta); commitErr != nil {
		return constraintBrainOutput(envelope, "belief_commit_requires_revision", commitErr.Error()), nil
	}
	delta.Committed = true
	return brainCapabilityOutput{Class: domain.ToolResultValidation, Status: "OK", Summary: "subjective belief delta accepted", BeliefDelta: &delta, NewInformation: delta.PreviousConfidence != delta.NewConfidence, Provenance: baseBrainProvenance(envelope, "belief-commit-v1", delta)}, nil
}

func runBrainSubmitDiagnosis(ctx context.Context, input submitBrainDiagnosisInput) (brainCapabilityOutput, error) {
	state, err := brainWorkflowState(ctx)
	if err != nil {
		return brainCapabilityOutput{}, err
	}
	if state.BrainPhase != domain.BrainPhaseDiagnosis {
		return phaseConstraintOutput(state, "submit_diagnosis", domain.BrainToolReasoning, input.Intent, domain.BrainPhaseDiagnosis), nil
	}
	envelope := newBrainEnvelope(state, "submit_diagnosis", domain.BrainToolReasoning, domain.AgentActionIntent{Intent: input.Intent, HypothesisIDs: []string{input.HypothesisID}, TargetScope: input.Targets, ExpectedObservation: input.ExpectedObservation})
	if denied := authorizeBrainTool(state, envelope); denied != nil {
		return *denied, nil
	}
	hypothesis, ok := findAgentHypothesis(state.AgentHypotheses, input.HypothesisID)
	if !ok {
		return constraintBrainOutput(envelope, "unknown_hypothesis_revision", "selected hypothesis revision does not exist"), nil
	}
	if input.Statement != hypothesis.Statement || input.Category != hypothesis.Category || input.Mechanism != hypothesis.Mechanism || brainruntime.Hash(input.Targets) != brainruntime.Hash(hypothesis.TargetRefs) {
		return constraintBrainOutput(envelope, "diagnosis_requires_hypothesis_revision", "diagnosis semantics must match the selected immutable hypothesis revision"), nil
	}
	if input.ModelConfidence != hypothesis.ModelConfidence {
		return constraintBrainOutput(envelope, "diagnosis_requires_belief_commit", "diagnosis confidence must match the latest committed hypothesis belief"), nil
	}
	grounding, grounded := findHypothesisGrounding(state.HypothesisGroundings, input.HypothesisID)
	if !grounded || !brainContainsString(input.ValidationResultIDs, grounding.ID) {
		return constraintBrainOutput(envelope, "diagnosis_missing_current_validation", "diagnosis must cite the current hypothesis grounding result"), nil
	}
	evidenceByID := map[string]bool{}
	for _, item := range state.Incident.Evidence {
		evidenceByID[item.ID] = true
	}
	for _, id := range input.EvidenceIDs {
		if !evidenceByID[id] {
			return constraintBrainOutput(envelope, "diagnosis_unknown_evidence", "diagnosis cited an evidence ID that is not in the current Runtime snapshot"), nil
		}
	}
	diagnosis := domain.AgentDiagnosis{ID: "diagnosis:" + ulid.Make().String(), HypothesisRevisionID: input.HypothesisID, Statement: input.Statement, Category: input.Category, Mechanism: input.Mechanism, TargetRefs: input.Targets, ModelConfidence: input.ModelConfidence, EvidenceIDs: append([]string(nil), input.EvidenceIDs...), ValidationResultIDs: append([]string(nil), input.ValidationResultIDs...), EvidenceSnapshotHash: state.EvidenceSnapshotHash, ExecutionSnapshot: state.ExecutionSnapshot, GroundingLevel: domain.GroundingUnknown, Provisional: true, SubmittedAt: time.Now().UTC()}
	return brainCapabilityOutput{Class: domain.ToolResultValidation, Status: "PERSISTED", Summary: "LLM diagnosis persisted unchanged; validate_diagnosis must append Runtime grounding before termination or recovery", Diagnosis: &diagnosis, NewInformation: true, Provenance: baseBrainProvenance(envelope, "diagnosis-persistence-v1", diagnosis)}, nil
}

func runBrainValidateDiagnosis(ctx context.Context, input validateBrainDiagnosisInput) (brainCapabilityOutput, error) {
	state, err := brainWorkflowState(ctx)
	if err != nil {
		return brainCapabilityOutput{}, err
	}
	if state.BrainPhase != domain.BrainPhaseDiagnosis {
		return phaseConstraintOutput(state, "validate_diagnosis", domain.BrainToolReasoning, input.Intent, domain.BrainPhaseDiagnosis), nil
	}
	hypothesisID := ""
	if state.AgentDiagnosis != nil {
		hypothesisID = state.AgentDiagnosis.HypothesisRevisionID
	}
	envelope := newBrainEnvelope(state, "validate_diagnosis", domain.BrainToolReasoning, domain.AgentActionIntent{Intent: input.Intent, HypothesisIDs: []string{hypothesisID}, ExpectedObservation: input.ExpectedObservation})
	if denied := authorizeBrainTool(state, envelope); denied != nil {
		return *denied, nil
	}
	validation := domain.DiagnosisValidation{ID: "diagnosis-validation:" + ulid.Make().String(), DiagnosisID: input.DiagnosisID, HypothesisRevisionID: hypothesisID, GroundingLevel: domain.GroundingUnknown, EvidenceSnapshotHash: state.EvidenceSnapshotHash, ExecutionSnapshot: state.ExecutionSnapshot, ValidatedAt: time.Now().UTC(), Provisional: true}
	if state.AgentDiagnosis == nil || state.AgentDiagnosis.ID != input.DiagnosisID {
		validation.ReasonCodes = append(validation.ReasonCodes, "diagnosis_not_found")
		return brainCapabilityOutput{Class: domain.ToolResultValidation, Status: "REJECTED", Summary: "diagnosis validation failed", DiagnosisValidation: &validation, Provenance: baseBrainProvenance(envelope, "diagnosis-validation-v1", validation)}, nil
	}
	diagnosis := *state.AgentDiagnosis
	hypothesis, hypothesisOK := findAgentHypothesis(state.AgentHypotheses, diagnosis.HypothesisRevisionID)
	admission, admissionOK := findHypothesisAdmission(state.HypothesisAdmissions, diagnosis.HypothesisRevisionID)
	grounding, groundingOK := findHypothesisGrounding(state.HypothesisGroundings, diagnosis.HypothesisRevisionID)
	if !hypothesisOK {
		validation.ReasonCodes = append(validation.ReasonCodes, "hypothesis_not_found")
	}
	if !admissionOK || admission.Decision != "ADMITTED" {
		validation.ReasonCodes = append(validation.ReasonCodes, "hypothesis_not_admitted")
	}
	if !groundingOK || !brainContainsString(diagnosis.ValidationResultIDs, grounding.ID) {
		validation.ReasonCodes = append(validation.ReasonCodes, "current_grounding_not_cited")
	}
	if groundingOK && grounding.EvidenceSnapshotHash != state.EvidenceSnapshotHash {
		validation.ReasonCodes = append(validation.ReasonCodes, "grounding_snapshot_stale")
	}
	if diagnosis.EvidenceSnapshotHash != state.EvidenceSnapshotHash {
		validation.ReasonCodes = append(validation.ReasonCodes, "diagnosis_snapshot_stale")
	}
	if diagnosis.ExecutionSnapshot != state.ExecutionSnapshot {
		validation.ReasonCodes = append(validation.ReasonCodes, "execution_snapshot_mismatch")
	}
	if hypothesisOK && (diagnosis.Statement != hypothesis.Statement || diagnosis.Category != hypothesis.Category || diagnosis.Mechanism != hypothesis.Mechanism || diagnosis.ModelConfidence != hypothesis.ModelConfidence || brainruntime.Hash(diagnosis.TargetRefs) != brainruntime.Hash(hypothesis.TargetRefs)) {
		validation.ReasonCodes = append(validation.ReasonCodes, "diagnosis_semantics_changed")
	}
	evidenceByID := map[string]bool{}
	for _, item := range state.Incident.Evidence {
		evidenceByID[item.ID] = true
	}
	for _, id := range diagnosis.EvidenceIDs {
		if !evidenceByID[id] {
			validation.ReasonCodes = append(validation.ReasonCodes, "unknown_evidence_id")
			break
		}
	}
	validation.Valid = len(validation.ReasonCodes) == 0
	if groundingOK {
		validation.GroundingLevel = grounding.Level
	}
	validation.Provisional = !validation.Valid || validation.GroundingLevel != domain.GroundingSupported
	diagnosis.GroundingLevel = validation.GroundingLevel
	diagnosis.Provisional = validation.Provisional
	status := "VALIDATED"
	if !validation.Valid {
		status = "REJECTED"
	}
	return brainCapabilityOutput{Class: domain.ToolResultValidation, Status: status, Summary: "Runtime diagnosis validation appended without modifying LLM diagnosis semantics", Diagnosis: &diagnosis, DiagnosisValidation: &validation, DiagnosisFinalized: validation.Valid, NewInformation: true, Provenance: baseBrainProvenance(envelope, "diagnosis-validation-v1", validation)}, nil
}

func runBrainSubmitRecovery(ctx context.Context, input submitBrainRecoveryInput) (brainCapabilityOutput, error) {
	state, err := brainWorkflowState(ctx)
	if err != nil {
		return brainCapabilityOutput{}, err
	}
	if state.BrainPhase != domain.BrainPhaseRecovery {
		return phaseConstraintOutput(state, "submit_recovery_plan", domain.BrainToolRecovery, input.Intent, domain.BrainPhaseRecovery), nil
	}
	envelope := newBrainEnvelope(state, "submit_recovery_plan", domain.BrainToolRecovery, domain.AgentActionIntent{Intent: input.Intent, ExpectedObservation: input.ExpectedObservation})
	if denied := authorizeBrainTool(state, envelope); denied != nil {
		return *denied, nil
	}
	if state.AgentDiagnosis == nil {
		return constraintBrainOutput(envelope, "diagnosis_required", "a persisted diagnosis is required before recovery planning"), nil
	}
	if len(input.Alternatives) > 3 {
		return constraintBrainOutput(envelope, "too_many_recovery_alternatives", "at most three recovery alternatives are allowed"), nil
	}
	plan := domain.AgentRecoveryPlan{ID: "recovery-plan:" + ulid.Make().String(), Goal: input.Goal, PrimaryAction: input.PrimaryAction, Alternatives: input.Alternatives, ExpectedOutcome: input.ExpectedOutcome, RollbackPlan: input.RollbackPlan, VerificationPlan: input.VerificationPlan, RiskReason: input.RiskReason, DiagnosisVersion: state.AgentDiagnosis.ID, EvidenceSnapshotHash: state.EvidenceSnapshotHash, ExecutionSnapshot: state.ExecutionSnapshot}
	return brainCapabilityOutput{Class: domain.ToolResultValidation, Status: "OK", Summary: "recovery plan recorded for Safety Kernel validation; no mutation executed", RecoveryPlan: &plan, NewInformation: true, Provenance: baseBrainProvenance(envelope, "recovery-plan-v1", plan)}, nil
}

func runBrainRequestSkills(ctx context.Context, resolver *BrainSkillResolver, input requestBrainSkillsInput) (brainCapabilityOutput, error) {
	state, err := brainWorkflowState(ctx)
	if err != nil {
		return brainCapabilityOutput{}, err
	}
	envelope := newBrainEnvelope(state, "request_skills", domain.BrainToolControl, domain.AgentActionIntent{Intent: input.Intent, ExpectedObservation: input.ExpectedObservation})
	if denied := authorizeBrainTool(state, envelope); denied != nil {
		return *denied, nil
	}
	accepted, rejected, resolved, resolveErr := resolveBrainSkillRequests(state, resolver, input)
	if resolveErr != nil {
		return constraintBrainOutput(envelope, "skill_resolution_failed", resolveErr.Error()), nil
	}
	if len(accepted) == 0 {
		return brainCapabilityOutput{Class: domain.ToolResultConstraint, Status: "REJECTED", Summary: skillActivationSummary(nil, rejected), ConstraintCode: "skill_request_not_activated", SkillActivations: rejected, NewInformation: false, Provenance: baseBrainProvenance(envelope, "skill-resolver-v1", resolved.Activations)}, nil
	}
	selectedCategory := resolver.unambiguousRequestedCategory(accepted)
	return brainCapabilityOutput{Class: domain.ToolResultValidation, Status: "OK", Summary: skillActivationSummary(accepted, rejected), RequestedSkills: accepted, SkillActivations: rejected, SelectedCategory: selectedCategory, NewInformation: true, Provenance: baseBrainProvenance(envelope, "skill-resolver-v1", resolved.Activations)}, nil
}

func resolveBrainSkillRequests(state *WorkflowState, resolver *BrainSkillResolver, input requestBrainSkillsInput) ([]SkillRequest, []domain.SkillActivation, ResolvedBrainSkills, error) {
	requests := make([]SkillRequest, 0, len(input.SkillIDs))
	for _, id := range input.SkillIDs {
		requests = append(requests, SkillRequest{SkillID: id, Reason: input.Reason, Trigger: input.Trigger, RequestedBy: "BRAIN", RequestedTurn: currentBrainTurnID(state)})
	}
	maxOptional := state.BrainBudget.Limits.MaxOptionalSkillsPerTurn
	if method, _ := domain.NormalizeDiagnosisMethod(state.Incident.DiagnosisMethod); method == domain.DiagnosisMethodKubePilotNoOptionalSkills {
		maxOptional = 0
	}
	// A reflection turn may repair an investigation constraint by requesting a
	// phase-specific Skill for the turn that resumes after reflection. Resolve
	// that request against the resume phase; resolving against REFLECTION would
	// reject every evidence Skill as phase-incompatible and trap the Brain in a
	// constraint/reflection loop even though the requested capability is valid
	// for the pending investigation.
	resolutionPhase := state.BrainPhase
	if resolutionPhase == domain.BrainPhaseReflection && state.ResumeBrainPhase != "" {
		resolutionPhase = state.ResumeBrainPhase
	}
	resolved, err := resolver.Resolve(resolutionPhase, requests, maxOptional)
	if err != nil {
		return nil, nil, ResolvedBrainSkills{}, err
	}
	accepted := []SkillRequest{}
	rejected := []domain.SkillActivation{}
	for _, request := range requests {
		matched := false
		for _, activation := range resolved.Activations {
			if activation.SkillID != request.SkillID {
				continue
			}
			matched = true
			if activation.Status == "ACTIVATED" {
				accepted = append(accepted, request)
			} else {
				rejected = append(rejected, activation)
			}
			break
		}
		if !matched {
			rejected = append(rejected, domain.SkillActivation{SkillID: request.SkillID, Phase: resolutionPhase, Reason: request.Reason, Trigger: request.Trigger, RequestedBy: request.RequestedBy, RequestedTurn: request.RequestedTurn, Status: "REJECTED", RejectedReason: "activation_decision_missing", ActivatedAt: time.Now().UTC()})
		}
	}
	return accepted, rejected, resolved, nil
}

func skillActivationSummary(accepted []SkillRequest, rejected []domain.SkillActivation) string {
	activatedIDs := make([]string, 0, len(accepted))
	for _, request := range accepted {
		activatedIDs = append(activatedIDs, request.SkillID)
	}
	sort.Strings(activatedIDs)
	rejectedDecisions := make([]string, 0, len(rejected))
	for _, activation := range rejected {
		rejectedDecisions = append(rejectedDecisions, activation.SkillID+":"+activation.RejectedReason)
	}
	sort.Strings(rejectedDecisions)
	parts := []string{}
	if len(activatedIDs) > 0 {
		parts = append(parts, "activated Skills: "+strings.Join(activatedIDs, ", "))
	}
	if len(rejectedDecisions) > 0 {
		parts = append(parts, "rejected Skills: "+strings.Join(rejectedDecisions, ", "))
	}
	if len(parts) == 0 {
		return "no Skill activation decision was produced"
	}
	return strings.Join(parts, "; ")
}

func runBrainReadSkillReference(ctx context.Context, resolver *BrainSkillResolver, input readBrainSkillReferenceInput) (brainCapabilityOutput, error) {
	state, err := brainWorkflowState(ctx)
	if err != nil {
		return brainCapabilityOutput{}, err
	}
	envelope := newBrainEnvelope(state, "read_skill_reference", domain.BrainToolControl, domain.AgentActionIntent{Intent: input.Intent, ExpectedObservation: input.ExpectedObservation})
	if denied := authorizeBrainTool(state, envelope); denied != nil {
		return *denied, nil
	}
	content, readErr := resolver.ReadActiveReference(state.ActiveSkillRefs, input.SkillID, input.Reference)
	if readErr != nil {
		return constraintBrainOutput(envelope, "skill_reference_denied", readErr.Error()), nil
	}
	return brainCapabilityOutput{Class: domain.ToolResultValidation, Status: "OK", Summary: "active Skill reference loaded", ReferenceContent: content, ReferenceID: input.SkillID + "/" + input.Reference, NewInformation: true, Provenance: baseBrainProvenance(envelope, "skill-reference-v1", content)}, nil
}

func runBrainSelectCategory(ctx context.Context, resolver *BrainSkillResolver, input selectBrainCategoryInput) (brainCapabilityOutput, error) {
	state, err := brainWorkflowState(ctx)
	if err != nil {
		return brainCapabilityOutput{}, err
	}
	envelope := newBrainEnvelope(state, "select_tool_category", domain.BrainToolControl, domain.AgentActionIntent{Intent: input.Intent, ExpectedObservation: input.ExpectedObservation})
	if denied := authorizeBrainTool(state, envelope); denied != nil {
		return *denied, nil
	}
	request := requestBrainSkillsInput{Intent: input.Intent, ExpectedObservation: input.ExpectedObservation, SkillIDs: input.SkillIDs, Reason: input.Reason, Trigger: input.Trigger}
	accepted, rejected, resolved, resolveErr := resolveBrainSkillRequests(state, resolver, request)
	if resolveErr != nil {
		return constraintBrainOutput(envelope, "skill_resolution_failed", resolveErr.Error()), nil
	}
	if len(accepted) == 0 {
		return brainCapabilityOutput{Class: domain.ToolResultConstraint, Status: "REJECTED", Summary: skillActivationSummary(nil, rejected), ConstraintCode: "skill_request_not_activated", SkillActivations: rejected, NewInformation: false, Provenance: baseBrainProvenance(envelope, "skill-category-router-v1", resolved.Activations)}, nil
	}
	// Dependencies may be broad routing Skills (for example select-tools). They
	// must not lend one of their categories to a narrower requested Skill. Only
	// the exact IDs chosen by the Brain may grant the selected category.
	if !brainRequestedSkillsGrantCategory(resolver, accepted, input.Category) {
		for _, request := range accepted {
			rejected = append(rejected, domain.SkillActivation{SkillID: request.SkillID, Phase: state.BrainPhase, Reason: request.Reason, Trigger: request.Trigger, RequestedBy: request.RequestedBy, RequestedTurn: request.RequestedTurn, Status: "REJECTED", RejectedReason: "requested_skill_does_not_grant_category", ActivatedAt: time.Now().UTC()})
		}
		return brainCapabilityOutput{Class: domain.ToolResultConstraint, Status: "REJECTED", Summary: skillActivationSummary(nil, rejected), ConstraintCode: "tool_category_not_granted_by_requested_skill", SkillActivations: rejected, NewInformation: false, Provenance: baseBrainProvenance(envelope, "skill-category-router-v1", resolved.Activations)}, nil
	}
	summary := skillActivationSummary(accepted, rejected) + "; selected Tool Category: " + string(input.Category)
	return brainCapabilityOutput{Class: domain.ToolResultValidation, Status: "OK", Summary: summary, RequestedSkills: accepted, SkillActivations: rejected, SelectedCategory: input.Category, NewInformation: true, Provenance: baseBrainProvenance(envelope, "skill-category-router-v1", resolved.Activations)}, nil
}

func brainRequestedSkillsGrantCategory(resolver *BrainSkillResolver, requests []SkillRequest, category domain.BrainToolCategory) bool {
	if resolver == nil || len(requests) == 0 {
		return false
	}
	for _, request := range requests {
		pkg, ok := resolver.packages[request.SkillID]
		if !ok {
			continue
		}
		for _, granted := range pkg.Spec.AllowedToolCategories {
			if granted == category {
				return true
			}
		}
	}
	return false
}

func runBrainAdvancePhase(ctx context.Context, input advanceBrainPhaseInput) (brainCapabilityOutput, error) {
	state, err := brainWorkflowState(ctx)
	if err != nil {
		return brainCapabilityOutput{}, err
	}
	envelope := newBrainEnvelope(state, "advance_brain_phase", domain.BrainToolControl, domain.AgentActionIntent{Intent: input.Intent, ExpectedObservation: input.ExpectedObservation})
	if denied := authorizeBrainTool(state, envelope); denied != nil {
		return *denied, nil
	}
	if state.BrainPhase != domain.BrainPhaseInvestigation || input.NextPhase != domain.BrainPhaseDiagnosis {
		return constraintBrainOutput(envelope, "invalid_phase_transition", "only INVESTIGATION to DIAGNOSIS is available through this control boundary"), nil
	}
	admitted, validated := 0, 0
	for _, admission := range state.HypothesisAdmissions {
		if admission.Decision == "ADMITTED" {
			admitted++
		}
	}
	for _, grounding := range state.HypothesisGroundings {
		if grounding.Level != domain.GroundingUnknown {
			validated++
		}
	}
	if admitted < 2 || validated < 2 {
		return constraintBrainOutput(envelope, "diagnosis_phase_preconditions", "at least two admitted and validated hypotheses are required before diagnosis synthesis"), nil
	}
	return brainCapabilityOutput{Class: domain.ToolResultValidation, Status: "OK", Summary: "diagnosis phase preconditions satisfied", NextPhase: domain.BrainPhaseDiagnosis, NewInformation: true, Provenance: baseBrainProvenance(envelope, "phase-router-v1", input.NextPhase)}, nil
}

func phaseConstraintOutput(state *WorkflowState, toolName string, category domain.BrainToolCategory, intent string, required domain.BrainPhase) brainCapabilityOutput {
	envelope := newBrainEnvelope(state, toolName, category, domain.AgentActionIntent{Intent: intent})
	return constraintBrainOutput(envelope, "tool_incompatible_with_phase", fmt.Sprintf("%s requires Brain phase %s", toolName, required))
}

func runBrainFinish(ctx context.Context, input finishBrainInput) (brainCapabilityOutput, error) {
	state, err := brainWorkflowState(ctx)
	if err != nil {
		return brainCapabilityOutput{}, err
	}
	envelope := newBrainEnvelope(state, "finish_investigation", domain.BrainToolControl, domain.AgentActionIntent{Intent: input.Intent, HypothesisIDs: []string{input.HypothesisID}, ExpectedObservation: input.ExpectedObservation})
	if denied := authorizeBrainTool(state, envelope); denied != nil {
		return *denied, nil
	}
	if code, reason := validateBrainTerminationRequest(state, input); code != "" {
		return constraintBrainOutput(envelope, code, reason), nil
	}
	termination, terminateErr := brainruntime.NewTermination(input.Reason, currentBrainTurnID(state), input.HypothesisID, state.EvidenceSnapshotHash, &state.ExecutionSnapshot, input.UnresolvedGaps, state.BrainBudget)
	if terminateErr != nil {
		return constraintBrainOutput(envelope, "invalid_termination", terminateErr.Error()), nil
	}
	return brainCapabilityOutput{Class: domain.ToolResultValidation, Status: "OK", Summary: "Brain loop termination requested", Termination: &termination, NewInformation: true, Provenance: baseBrainProvenance(envelope, "termination-router-v1", termination)}, nil
}

func validateBrainTerminationRequest(state *WorkflowState, input finishBrainInput) (string, string) {
	switch input.Reason {
	case domain.TerminationHumanEscalation:
		return "", ""
	case domain.TerminationDiagnosisConfident:
		if state.AgentDiagnosis == nil || state.AgentDiagnosis.Provisional || input.HypothesisID != state.AgentDiagnosis.HypothesisRevisionID {
			return "termination_preconditions", "confident termination requires the current non-provisional LLM diagnosis revision"
		}
		return "", ""
	case domain.TerminationDiagnosisProvisional:
		if state.AgentDiagnosis == nil || !state.AgentDiagnosis.Provisional || input.HypothesisID != state.AgentDiagnosis.HypothesisRevisionID {
			return "termination_preconditions", "provisional termination requires the current provisional LLM diagnosis revision"
		}
		return "", ""
	case domain.TerminationEvidenceSaturated:
		noInformation := 0
		for index := len(state.ToolExecutions) - 1; index >= 0; index-- {
			if state.ToolExecutions[index].Envelope.ToolCategory == domain.BrainToolControl {
				continue
			}
			if state.ToolExecutions[index].Result.NewInformation {
				break
			}
			noInformation++
		}
		if noInformation < state.BrainToolPolicy.MaxNoInformationStreak || len(state.RequestedSkills) > 0 {
			return "evidence_not_saturated", "evidence saturation requires the configured no-information streak and no pending Skill request"
		}
		return "", ""
	default:
		return "runtime_owned_termination_reason", "approval, safety, budget, recovery, verification, and infrastructure outcomes are owned by the Runtime"
	}
}

func newBrainEnvelope(state *WorkflowState, toolName string, category domain.BrainToolCategory, intent domain.AgentActionIntent) domain.AgentActionEnvelope {
	id := ulid.Make().String()
	return domain.AgentActionEnvelope{ActionID: id, IncidentID: state.Incident.ID, TurnID: currentBrainTurnID(state), Phase: state.BrainPhase, ToolName: toolName, ToolCategory: category, RoutedToolCategory: currentBrainToolCategory(state), SkillRefs: append([]domain.SkillRef(nil), state.ActiveSkillRefs...), EvidenceSnapshotHash: state.EvidenceSnapshotHash, IdempotencyKey: brainruntime.Hash(struct {
		Incident, Turn, Tool string
		Intent               domain.AgentActionIntent
	}{state.Incident.ID, currentBrainTurnID(state), toolName, intent}), Intent: intent}
}

func authorizeBrainTool(state *WorkflowState, envelope domain.AgentActionEnvelope) *brainCapabilityOutput {
	if state != nil && state.BrainBudget.ToolCallsExhausted && !isToolBudgetClosingAction(envelope.ToolName) {
		return pointer(constraintBrainOutput(envelope, "tool_call_budget_exhausted", "investigation ToolCall budget is exhausted; only submit_diagnosis, validate_diagnosis, or finish_investigation may use the existing state"))
	}
	allowed := map[domain.BrainToolCategory]bool{domain.BrainToolControl: true}
	for _, category := range allowedCategoriesForState(state) {
		allowed[category] = true
	}
	admitted := false
	for _, item := range state.HypothesisAdmissions {
		admitted = admitted || item.Decision == "ADMITTED"
	}
	decision := (brainruntime.ToolPolicy{Policy: stateToolPolicy(state)}).Validate(envelope, state.ToolExecutions, admitted, allowed)
	if decision.Allowed {
		return nil
	}
	return pointer(constraintBrainOutput(envelope, strings.Join(decision.ReasonCodes, ","), "Tool Calling Policy rejected the request"))
}

func stateToolPolicy(state *WorkflowState) domain.ToolCallingPolicy {
	if state == nil || state.BrainBudget.Limits.MaxTurns == 0 {
		return brainruntime.DefaultToolCallingPolicy()
	}
	return state.BrainToolPolicy
}

func allowedCategoriesForState(state *WorkflowState) []domain.BrainToolCategory {
	if state.ActiveToolCategory == "" {
		return []domain.BrainToolCategory{domain.BrainToolReasoning}
	}
	for _, category := range state.ActiveSkillCategories {
		if category == state.ActiveToolCategory {
			return []domain.BrainToolCategory{state.ActiveToolCategory}
		}
	}
	return nil
}

func availableBrainCategories(state *WorkflowState) []domain.BrainToolCategory {
	return []domain.BrainToolCategory{domain.BrainToolEvidence, domain.BrainToolRetrieval, domain.BrainToolReasoning}
}

func constraintBrainOutput(envelope domain.AgentActionEnvelope, code, summary string) brainCapabilityOutput {
	return brainCapabilityOutput{Class: domain.ToolResultConstraint, Status: "REJECTED", Summary: summary, ConstraintCode: code, Provenance: baseBrainProvenance(envelope, "constraint-kernel-v1", code)}
}

func errorBrainOutput(envelope domain.AgentActionEnvelope, summary string, infrastructure bool) brainCapabilityOutput {
	return brainCapabilityOutput{Class: domain.ToolResultError, Status: "ERROR", Summary: summary, Infrastructure: infrastructure, Provenance: baseBrainProvenance(envelope, "tool-error-v1", summary)}
}

func baseBrainProvenance(envelope domain.AgentActionEnvelope, collector string, artifact any) domain.ToolResultProvenance {
	return domain.ToolResultProvenance{ToolName: envelope.ToolName, Collector: collector, TargetRefs: append([]domain.ResourceRef(nil), envelope.Intent.TargetScope...), ObservedAt: time.Now().UTC(), RawArtifactHash: brainruntime.Hash(artifact), ParserVersion: "brain-runtime-v1"}
}

func pointer[T any](value T) *T { return &value }

func currentBrainTurnID(state *WorkflowState) string {
	if len(state.BrainTurns) == 0 {
		return ""
	}
	return state.BrainTurns[len(state.BrainTurns)-1].ID
}

func hasNewEvidence(existing, incoming []domain.Evidence) bool {
	seen := map[string]bool{}
	for _, item := range existing {
		seen[item.ID] = true
	}
	for _, item := range incoming {
		if item.ID != "" && !seen[item.ID] {
			return true
		}
	}
	return false
}

func evidenceIDs(items []domain.Evidence) []string {
	ids := []string{}
	for _, item := range items {
		if item.ID != "" {
			ids = append(ids, item.ID)
		}
	}
	sort.Strings(ids)
	return ids
}

func findAgentHypothesis(items []domain.AgentHypothesis, id string) (domain.AgentHypothesis, bool) {
	for index := len(items) - 1; index >= 0; index-- {
		if items[index].ID == id {
			return items[index], true
		}
	}
	return domain.AgentHypothesis{}, false
}

func findHypothesisAdmission(items []domain.HypothesisAdmission, id string) (domain.HypothesisAdmission, bool) {
	for index := len(items) - 1; index >= 0; index-- {
		if items[index].HypothesisRevisionID == id {
			return items[index], true
		}
	}
	return domain.HypothesisAdmission{}, false
}

func findHypothesisGrounding(items []domain.HypothesisGrounding, id string) (domain.HypothesisGrounding, bool) {
	for index := len(items) - 1; index >= 0; index-- {
		if items[index].HypothesisRevisionID == id {
			return items[index], true
		}
	}
	return domain.HypothesisGrounding{}, false
}

func decodeBrainCapabilityOutput(raw string) (brainCapabilityOutput, error) {
	var output brainCapabilityOutput
	if err := json.Unmarshal([]byte(raw), &output); err != nil {
		return output, err
	}
	return output, nil
}
