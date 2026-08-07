package brainruntime

import (
	"strings"
	"testing"
	"time"

	"github.com/kubepilot-aiops/kubepilot/internal/domain"
	"github.com/kubepilot-aiops/kubepilot/internal/topology"
)

func TestAdmissionChecksScopeAndVerifiabilityButNotMechanismPlausibility(t *testing.T) {
	incident := &domain.Incident{ID: "i", Namespace: "team-a", Service: "payment", Resource: "deployment/payment"}
	graph := &topology.IncidentGraph{Edges: []topology.GraphEdge{{Source: "payment", Target: "redis", Relation: "depends_on"}}}
	h := domain.AgentHypothesis{ID: "h1", Statement: "scheduler quantum inversion", Mechanism: "an unfamiliar mechanism", TargetRefs: []domain.ResourceRef{{Namespace: "team-a", Service: "redis"}}, EvidenceNeeds: []string{"dependency availability"}, FalsificationConditions: []string{"dependency is healthy"}}
	admission := (AdmissionService{}).Admit(h, AdmissionContext{Incident: incident, Graph: graph, AvailableToolCategories: []domain.BrainToolCategory{domain.BrainToolEvidence}})
	if admission.Decision != "ADMITTED" || admission.GroundingLevel != domain.AdmissionIndirect {
		t.Fatalf("unexpected admission: %+v", admission)
	}
	for _, reason := range admission.ReasonCodes {
		if strings.Contains(reason, "mechanism") || strings.Contains(reason, "ontology") {
			t.Fatalf("admission performed forbidden plausibility judgment: %v", admission.ReasonCodes)
		}
	}
	h.TargetRefs = []domain.ResourceRef{{Namespace: "other", Service: "redis"}}
	denied := (AdmissionService{}).Admit(h, AdmissionContext{Incident: incident, Graph: graph, AvailableToolCategories: []domain.BrainToolCategory{domain.BrainToolEvidence}})
	if denied.Decision != "REJECTED" || denied.ResourceScope[0].Reason != "cross_namespace_denied" {
		t.Fatalf("cross namespace target was not rejected: %+v", denied)
	}
	h.TargetRefs = []domain.ResourceRef{{Kind: "ExternalService", Service: "payments-db"}}
	external := (AdmissionService{}).Admit(h, AdmissionContext{Incident: incident, ExternalInventory: []domain.ResourceRef{{Kind: "ExternalService", Service: "payments-db"}}, AvailableToolCategories: []domain.BrainToolCategory{domain.BrainToolEvidence}})
	if external.Decision != "ADMITTED" || external.GroundingLevel != domain.AdmissionIndirect || external.ResourceScope[0].Reason != "registered_external_inventory" {
		t.Fatalf("registered external inventory target was not admitted as indirect scope: %+v", external)
	}
}

func TestToolPolicyRejectsMissingBindingRepeatsAndNoInformation(t *testing.T) {
	policy := ToolPolicy{Policy: DefaultToolCallingPolicy()}
	envelope := domain.AgentActionEnvelope{ActionID: "a", IncidentID: "i", ToolName: "query_metrics", ToolCategory: domain.BrainToolEvidence, Intent: domain.AgentActionIntent{Intent: "test saturation", ExpectedObservation: []string{"cpu_pressure"}, TargetScope: []domain.ResourceRef{{Namespace: "ns", Service: "api"}}}}
	decision := policy.Validate(envelope, nil, true, map[domain.BrainToolCategory]bool{domain.BrainToolEvidence: true})
	if decision.Allowed || !contains(decision.ReasonCodes, "missing_hypothesis_binding") {
		t.Fatalf("expected missing binding rejection: %+v", decision)
	}
	envelope.Intent.HypothesisIDs = []string{"h1"}
	if decision = policy.Validate(envelope, nil, true, map[domain.BrainToolCategory]bool{domain.BrainToolEvidence: true}); !decision.Allowed {
		t.Fatalf("valid request rejected: %+v", decision)
	}
	history := []domain.BrainToolExecution{{Envelope: envelope, Result: domain.ToolResultRecord{Class: domain.ToolResultEvidence, NewInformation: false}}, {Envelope: withActionID(envelope, "b"), Result: domain.ToolResultRecord{Class: domain.ToolResultEvidence, NewInformation: false}}}
	decision = policy.Validate(withActionID(envelope, "c"), history, true, map[domain.BrainToolCategory]bool{domain.BrainToolEvidence: true})
	if decision.Allowed || !contains(decision.ReasonCodes, "exact_request_repeat") || !contains(decision.ReasonCodes, "no_information_streak") {
		t.Fatalf("repeat/no-information policy missing: %+v", decision)
	}
}

func TestToolPolicyAllowsConstraintRetryAfterRoutedCategoryChanges(t *testing.T) {
	policy := ToolPolicy{Policy: DefaultToolCallingPolicy()}
	request := domain.AgentActionEnvelope{
		ActionID: "wrong-route", IncidentID: "i", ToolName: "discover_resources",
		ToolCategory: domain.BrainToolEvidence, RoutedToolCategory: domain.BrainToolRetrieval,
		Intent: domain.AgentActionIntent{
			Intent: "resolve one-hop dependencies", ExpectedObservation: []string{"typed resource identities"},
			HypothesisIDs: []string{"h1"}, TargetScope: []domain.ResourceRef{{Namespace: "ns", Service: "api"}},
		},
	}
	history := []domain.BrainToolExecution{{Envelope: request, Result: domain.ToolResultRecord{Class: domain.ToolResultConstraint, ConstraintCode: "tool_not_available_in_category"}}}
	unchanged := request
	unchanged.ActionID = "unchanged-route"
	decision := policy.Validate(unchanged, history, true, map[domain.BrainToolCategory]bool{domain.BrainToolEvidence: true})
	if decision.Allowed || !contains(decision.ReasonCodes, "unchanged_constraint_retry") {
		t.Fatalf("unchanged routed constraint retry was not rejected: %+v", decision)
	}
	corrected := request
	corrected.ActionID = "correct-route"
	corrected.RoutedToolCategory = domain.BrainToolEvidence
	decision = policy.Validate(corrected, history, true, map[domain.BrainToolCategory]bool{domain.BrainToolEvidence: true})
	if !decision.Allowed {
		t.Fatalf("request corrected to the exposed Tool Category was rejected as an exact repeat: %+v", decision)
	}
}

func TestToolPolicyRequiresExpectedObservationForControlTools(t *testing.T) {
	policy := ToolPolicy{Policy: DefaultToolCallingPolicy()}
	envelope := domain.AgentActionEnvelope{
		ActionID:     "a",
		IncidentID:   "i",
		ToolName:     "select_tool_category",
		ToolCategory: domain.BrainToolControl,
		Intent:       domain.AgentActionIntent{Intent: "select the bounded evidence capability"},
	}
	decision := policy.Validate(envelope, nil, false, map[domain.BrainToolCategory]bool{domain.BrainToolControl: true})
	if decision.Allowed || !contains(decision.ReasonCodes, "missing_expected_observation") {
		t.Fatalf("control tool escaped expected-observation policy: %+v", decision)
	}
}

func TestToolPolicyBindsValidationAndAllowsRevalidationAfterEvidenceChanges(t *testing.T) {
	policy := ToolPolicy{Policy: DefaultToolCallingPolicy()}
	envelope := domain.AgentActionEnvelope{ActionID: "a", IncidentID: "i", ToolName: "validate_hypothesis", ToolCategory: domain.BrainToolReasoning, EvidenceSnapshotHash: "snapshot-a", Intent: domain.AgentActionIntent{Intent: "validate the selected mechanism", ExpectedObservation: []string{"grounding delta"}}}
	allowedCategories := map[domain.BrainToolCategory]bool{domain.BrainToolReasoning: true}
	decision := policy.Validate(envelope, nil, true, allowedCategories)
	if decision.Allowed || !contains(decision.ReasonCodes, "missing_hypothesis_binding") {
		t.Fatalf("validation tool escaped hypothesis binding: %+v", decision)
	}
	envelope.Intent.HypothesisIDs = []string{"h1"}
	if decision = policy.Validate(envelope, nil, true, allowedCategories); !decision.Allowed {
		t.Fatalf("bound validation was rejected: %+v", decision)
	}
	history := []domain.BrainToolExecution{{Envelope: envelope, Result: domain.ToolResultRecord{Class: domain.ToolResultValidation, NewInformation: true}}}
	revalidated := withActionID(envelope, "b")
	revalidated.EvidenceSnapshotHash = "snapshot-b"
	if decision = policy.Validate(revalidated, history, true, allowedCategories); !decision.Allowed {
		t.Fatalf("new evidence snapshot must permit revalidation: %+v", decision)
	}
}

func TestGroundingUsesServerIDsAndDoesNotChangeModelConfidence(t *testing.T) {
	now := time.Now().UTC()
	h := domain.AgentHypothesis{ID: "h1", EvidenceNeeds: []string{"resource pressure", "request impact"}, ModelConfidence: .73, TargetRefs: []domain.ResourceRef{{Namespace: "ns", Service: "api"}}}
	evidence := []domain.Evidence{
		{ID: "m", Source: "prometheus", QualityScore: .8, ObservedAt: now, CausalNodeIDs: []string{"pressure"}},
		{ID: "k", Source: "kubernetes", QualityScore: .9, ObservedAt: now, CausalNodeIDs: []string{"impact"}},
	}
	grounding, delta := (Grounder{}).Validate(h, evidence, ValidationInput{SupportingEvidenceIDs: []string{"m", "k", "invented"}, FulfilledEvidenceNeeds: []string{"resource pressure", "request impact"}, ExpectedCausalNodeIDs: []string{"pressure", "impact"}, CausalClaim: true, TargetScopeDecisions: []domain.ResourceScopeDecision{{Allowed: true}}, WindowStart: now.Add(-time.Minute), WindowEnd: now.Add(time.Minute)}, nil)
	if grounding.Level != domain.GroundingSupported || grounding.Evidence.IndependentSourceCount != 2 || len(grounding.Evidence.SupportingEvidenceIDs) != 2 {
		t.Fatalf("unexpected grounding: %+v", grounding)
	}
	if h.ModelConfidence != .73 || delta.CurrentLevel != domain.GroundingSupported {
		t.Fatalf("runtime changed subjective confidence or omitted delta")
	}
}

func TestCausalClaimWithoutObservedPathCannotBeSupported(t *testing.T) {
	hypothesis := domain.AgentHypothesis{ID: "h-causal", Mechanism: "causal mechanism", TargetRefs: []domain.ResourceRef{{Namespace: "team-a", Service: "api"}}, EvidenceNeeds: []string{"latency"}, ModelConfidence: .8}
	evidence := []domain.Evidence{{ID: "e1", Source: "prometheus", Confidence: .9, ObservedAt: time.Now().UTC()}}
	grounding, _ := (Grounder{}).Validate(hypothesis, evidence, ValidationInput{
		SupportingEvidenceIDs:  []string{"e1"},
		FulfilledEvidenceNeeds: []string{"latency"},
		CausalClaim:            true,
		TargetScopeDecisions:   []domain.ResourceScopeDecision{{Allowed: true}},
	}, nil)
	if grounding.Level == domain.GroundingSupported || grounding.Coverage.CausalPathCoverage != 0 {
		t.Fatalf("causal claim without a server-observed path was supported: %+v", grounding)
	}
}

func TestSnapshotInvalidationAndBeliefCommitBoundary(t *testing.T) {
	h := domain.AgentHypothesis{ID: "h1", Status: domain.HypothesisSupported, ModelConfidence: .8, LastValidatedSnapshotHash: "old"}
	stale := InvalidateStaleHypotheses([]domain.AgentHypothesis{h}, "new")
	if stale[0].Status != domain.HypothesisInvestigating {
		t.Fatalf("supported hypothesis was not invalidated: %+v", stale[0])
	}
	if _, err := CommitBelief(h, domain.BeliefDelta{HypothesisRevisionID: "h1", NewConfidence: .4, RevisionRequired: true}); err == nil {
		t.Fatal("semantic change must require a new revision")
	}
	updated, err := CommitBelief(h, domain.BeliefDelta{HypothesisRevisionID: "h1", NewConfidence: .4})
	if err != nil || updated.ModelConfidence != .4 {
		t.Fatalf("valid subjective confidence commit failed: %+v %v", updated, err)
	}
}

func TestRecoveryEligibilityRequiresCompleteGroundedChain(t *testing.T) {
	snapshot := domain.ExecutionSnapshot{SkillSnapshotHash: "s", ModelConfigHash: "m", ToolSchemaHash: "t", PolicyHash: "p"}
	h := domain.AgentHypothesis{ID: "h1", LastValidatedSnapshotHash: "e"}
	diagnosis := &domain.AgentDiagnosis{HypothesisRevisionID: "h1", EvidenceSnapshotHash: "e", ExecutionSnapshot: snapshot, GroundingLevel: domain.GroundingSupported}
	grounding := domain.HypothesisGrounding{HypothesisRevisionID: "h1", Level: domain.GroundingSupported, EvidenceSnapshotHash: "e", CausalCoverageApplicable: true, Evidence: domain.GroundingEvidence{EvidenceSupport: .9, ContradictionRatio: 0, IndependentSourceCount: 2}, Coverage: domain.GroundingCoverage{EvidenceNeedCoverage: 1, CausalPathCoverage: 1, TargetScopeCoverage: 1, TemporalCoverage: 1}}
	allowed := RecoveryAllowed(diagnosis, h, grounding, []domain.HypothesisGrounding{grounding, {HypothesisRevisionID: "h2"}}, snapshot, true, .2, 2)
	if !allowed.Allowed {
		t.Fatalf("complete grounded chain rejected: %+v", allowed)
	}
	diagnosis.Provisional = true
	denied := RecoveryAllowed(diagnosis, h, grounding, []domain.HypothesisGrounding{grounding, {HypothesisRevisionID: "h2"}}, snapshot, true, .2, 2)
	if denied.Allowed || !contains(denied.ReasonCodes, "diagnosis_not_final") {
		t.Fatalf("provisional diagnosis must not recover: %+v", denied)
	}
}

func withActionID(value domain.AgentActionEnvelope, id string) domain.AgentActionEnvelope {
	value.ActionID = id
	return value
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
