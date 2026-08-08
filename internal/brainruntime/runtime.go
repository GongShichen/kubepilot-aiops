package brainruntime

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/kubepilot-aiops/kubepilot/internal/domain"
	"github.com/kubepilot-aiops/kubepilot/internal/topology"
)

// DefaultMaxTurns is scoped to one LLM Brain Agent Workflow Attempt. It is
// neither an Incident wall-clock timeout nor a shared limit across agents.
const DefaultMaxTurns = 50

func DefaultBudget() domain.BrainBudget {
	return domain.BrainBudget{
		MaxTurns: DefaultMaxTurns, MaxActiveHypotheses: 5, MaxHypothesisBranches: 8,
		MaxRevisionsPerLineage: 3, MaxToolCalls: 50, MaxParallelReadTools: 4,
		MaxOptionalSkillsPerTurn: 2, MaxStructuredCorrections: 3,
		MaxReflectionCostUnits: 16,
	}
}

func DefaultToolCallingPolicy() domain.ToolCallingPolicy {
	return domain.ToolCallingPolicy{
		MaxSameToolRepeat: 3, MaxNoInformationStreak: 2,
		RequireReason: true, RequireExpectedObservation: true,
		RequireHypothesisBindingAfterAdmission: true,
		RejectExactRequestRepeat:               true, RejectUnchangedConstraintRetry: true,
	}
}

func Hash(value any) string {
	raw, _ := json.Marshal(value)
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}

func EvidenceSnapshotHash(evidence []domain.Evidence) string {
	type snapshotEvidence struct {
		ID          string                  `json:"id"`
		Source      string                  `json:"source"`
		ObservedAt  time.Time               `json:"observed_at"`
		WindowStart time.Time               `json:"window_start"`
		WindowEnd   time.Time               `json:"window_end"`
		Facts       map[string]any          `json:"facts,omitempty"`
		Signals     []domain.EvidenceSignal `json:"signals,omitempty"`
	}
	items := make([]snapshotEvidence, 0, len(evidence))
	for _, item := range evidence {
		observed := item.ObservedAt
		if observed.IsZero() {
			observed = item.Timestamp
		}
		items = append(items, snapshotEvidence{ID: item.ID, Source: item.Source, ObservedAt: observed, WindowStart: item.WindowStart, WindowEnd: item.WindowEnd, Facts: item.Facts, Signals: item.Signals})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	return Hash(items)
}

// ToolPolicy validates one model action envelope without interpreting any
// incident mechanism. Skill and graph allowlists are supplied by the caller.
type ToolPolicy struct{ Policy domain.ToolCallingPolicy }

func (p ToolPolicy) Validate(envelope domain.AgentActionEnvelope, history []domain.BrainToolExecution, admitted bool, allowedCategories map[domain.BrainToolCategory]bool) domain.ToolPolicyDecision {
	policy := p.Policy
	if policy.MaxSameToolRepeat == 0 {
		policy = DefaultToolCallingPolicy()
	}
	reasons := []string{}
	if strings.TrimSpace(envelope.ToolName) == "" || strings.TrimSpace(envelope.ActionID) == "" || strings.TrimSpace(envelope.IncidentID) == "" {
		reasons = append(reasons, "invalid_action_envelope")
	}
	if !allowedCategories[envelope.ToolCategory] {
		reasons = append(reasons, "tool_category_not_allowed")
	}
	if policy.RequireReason && strings.TrimSpace(envelope.Intent.Intent) == "" {
		reasons = append(reasons, "missing_intent")
	}
	if policy.RequireExpectedObservation && len(nonEmpty(envelope.Intent.ExpectedObservation)) == 0 {
		reasons = append(reasons, "missing_expected_observation")
	}
	if admitted && policy.RequireHypothesisBindingAfterAdmission && requiresHypothesisBinding(envelope) && len(nonEmpty(envelope.Intent.HypothesisIDs)) == 0 {
		reasons = append(reasons, "missing_hypothesis_binding")
	}
	fingerprint := actionFingerprint(envelope)
	if policy.RejectExactRequestRepeat {
		for _, previous := range history {
			if actionFingerprint(previous.Envelope) == fingerprint {
				reasons = append(reasons, "exact_request_repeat")
				break
			}
		}
	}
	repeats := 0
	for index := len(history) - 1; index >= 0; index-- {
		if history[index].Envelope.ToolName != envelope.ToolName || targetFingerprint(history[index].Envelope.Intent.TargetScope) != targetFingerprint(envelope.Intent.TargetScope) {
			break
		}
		repeats++
	}
	if repeats >= policy.MaxSameToolRepeat {
		reasons = append(reasons, "same_tool_target_repeat_limit")
	}
	noInfo := 0
	for index := len(history) - 1; index >= 0 && !history[index].Result.NewInformation; index-- {
		noInfo++
	}
	if noInfo >= policy.MaxNoInformationStreak && envelope.ToolCategory != domain.BrainToolControl {
		reasons = append(reasons, "no_information_streak")
	}
	if policy.RejectUnchangedConstraintRetry && len(history) > 0 {
		last := history[len(history)-1]
		if last.Result.Class == domain.ToolResultConstraint && actionFingerprint(last.Envelope) == fingerprint {
			reasons = append(reasons, "unchanged_constraint_retry")
		}
	}
	return domain.ToolPolicyDecision{Allowed: len(reasons) == 0, ReasonCodes: unique(reasons), Fingerprint: fingerprint}
}

func requiresHypothesisBinding(envelope domain.AgentActionEnvelope) bool {
	if envelope.ToolCategory == domain.BrainToolEvidence || envelope.ToolCategory == domain.BrainToolRetrieval {
		return true
	}
	// Reasoning tools that operate on a hypothesis are bound as strictly as
	// evidence collectors. Other reasoning tools (understanding, planning and
	// initial hypothesis submission) intentionally have no prior hypothesis.
	switch envelope.ToolName {
	case "validate_hypothesis", "compare_hypotheses", "revise_hypothesis", "revise_investigation_plan", "commit_belief_delta", "submit_diagnosis", "validate_diagnosis":
		return true
	default:
		return false
	}
}

func actionFingerprint(envelope domain.AgentActionEnvelope) string {
	return Hash(struct {
		ToolName             string                   `json:"tool_name"`
		Category             domain.BrainToolCategory `json:"category"`
		RoutedCategory       domain.BrainToolCategory `json:"routed_category"`
		Intent               domain.AgentActionIntent `json:"intent"`
		EvidenceSnapshotHash string                   `json:"evidence_snapshot_hash"`
	}{envelope.ToolName, envelope.ToolCategory, envelope.RoutedToolCategory, envelope.Intent, envelope.EvidenceSnapshotHash})
}

func targetFingerprint(targets []domain.ResourceRef) string {
	copyTargets := append([]domain.ResourceRef(nil), targets...)
	sort.Slice(copyTargets, func(i, j int) bool {
		return resourceKey(copyTargets[i]) < resourceKey(copyTargets[j])
	})
	return Hash(copyTargets)
}

// AdmissionContext contains only server-owned identities and capabilities.
// No ontology or mechanism plausibility input exists by design.
type AdmissionContext struct {
	Incident                *domain.Incident
	Graph                   *topology.IncidentGraph
	ExternalInventory       []domain.ResourceRef
	AvailableToolCategories []domain.BrainToolCategory
}

type AdmissionService struct{}

func (AdmissionService) Admit(hypothesis domain.AgentHypothesis, ctx AdmissionContext) domain.HypothesisAdmission {
	decision := domain.HypothesisAdmission{HypothesisRevisionID: hypothesis.ID, Decision: "ADMITTED", GroundingLevel: domain.AdmissionDirect, OccurredAt: time.Now().UTC()}
	for _, category := range ctx.AvailableToolCategories {
		decision.AllowedToolCategories = appendUniqueCategory(decision.AllowedToolCategories, category)
	}
	if strings.TrimSpace(hypothesis.ID) == "" || strings.TrimSpace(hypothesis.Statement) == "" || strings.TrimSpace(hypothesis.Mechanism) == "" || len(nonEmpty(hypothesis.EvidenceNeeds)) == 0 || len(nonEmpty(hypothesis.FalsificationConditions)) == 0 {
		decision.ReasonCodes = append(decision.ReasonCodes, "invalid_hypothesis_contract")
	}
	if ctx.Incident == nil {
		decision.ReasonCodes = append(decision.ReasonCodes, "missing_incident_scope")
	}
	if len(ctx.AvailableToolCategories) == 0 {
		decision.ReasonCodes = append(decision.ReasonCodes, "no_validation_tool_path")
	}
	if len(hypothesis.TargetRefs) == 0 {
		decision.ReasonCodes = append(decision.ReasonCodes, "missing_target")
	}
	for _, target := range hypothesis.TargetRefs {
		resolved := resolveTarget(target, ctx)
		decision.ResourceScope = append(decision.ResourceScope, resolved)
		if !resolved.Allowed {
			decision.ReasonCodes = append(decision.ReasonCodes, "target_out_of_scope")
		}
		if resolved.Reason == "one_hop_dependency" || resolved.Reason == "registered_external_inventory" {
			decision.GroundingLevel = domain.AdmissionIndirect
		}
	}
	if len(decision.ReasonCodes) > 0 {
		decision.Decision = "REJECTED"
		decision.GroundingLevel = domain.AdmissionRejected
	}
	decision.ReasonCodes = unique(decision.ReasonCodes)
	return decision
}

func resolveTarget(target domain.ResourceRef, ctx AdmissionContext) domain.ResourceScopeDecision {
	decision := domain.ResourceScopeDecision{Requested: target, Resolved: target}
	if ctx.Incident == nil {
		decision.Reason = "missing_incident"
		return decision
	}
	if target.Namespace != "" && target.Namespace != ctx.Incident.Namespace {
		decision.Reason = "cross_namespace_denied"
		return decision
	}
	namespace := target.Namespace
	if namespace == "" {
		namespace = ctx.Incident.Namespace
		decision.Resolved.Namespace = namespace
	}
	if namespace == ctx.Incident.Namespace && matchesIncidentTarget(target, ctx.Incident) {
		decision.Allowed = true
		decision.Reason = "incident_scope"
		return decision
	}
	if ctx.Graph != nil && isOneHopTarget(target, ctx.Incident, *ctx.Graph) {
		decision.Allowed = true
		decision.Reason = "one_hop_dependency"
		return decision
	}
	for _, registered := range ctx.ExternalInventory {
		if resourceRefMatches(target, registered) {
			decision.Allowed = true
			decision.Resolved = registered
			decision.Reason = "registered_external_inventory"
			return decision
		}
	}
	decision.Reason = "unresolved_target"
	return decision
}

func matchesIncidentTarget(target domain.ResourceRef, incident *domain.Incident) bool {
	values := []string{target.Service, target.Resource}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" && (value == incident.Service || value == incident.Resource) {
			return true
		}
	}
	return false
}

func isOneHopTarget(target domain.ResourceRef, incident *domain.Incident, graph topology.IncidentGraph) bool {
	wanted := map[string]bool{}
	for _, value := range []string{target.Service, target.Resource} {
		if value != "" {
			wanted[value] = true
		}
	}
	roots := map[string]bool{incident.Service: true, incident.Resource: true}
	for _, edge := range graph.Edges {
		if (roots[edge.Source] && wanted[edge.Target]) || (roots[edge.Target] && wanted[edge.Source]) {
			return true
		}
	}
	return false
}

func resourceRefMatches(a, b domain.ResourceRef) bool {
	if b.Namespace != "" && a.Namespace != "" && a.Namespace != b.Namespace {
		return false
	}
	return (a.Service != "" && a.Service == b.Service) || (a.Resource != "" && a.Resource == b.Resource)
}

type ValidationInput struct {
	Attributions           []domain.HypothesisEvidenceAttribution
	FulfilledEvidenceNeeds []string
	MissingObservations    []string
	ExpectedCausalNodeIDs  []string
	CausalClaim            bool
	TargetScopeDecisions   []domain.ResourceScopeDecision
	WindowStart            time.Time
	WindowEnd              time.Time
}

// ValidateEvidenceAttributions freezes the model's explicit Evidence
// interpretation without deciding whether the mechanism itself is plausible.
// Only current server Evidence IDs, the closed relation enum, bounded weights,
// and a non-empty explanation are admitted.
func ValidateEvidenceAttributions(hypothesisID, turnID string, evidence []domain.Evidence, intents []domain.EvidenceAttributionIntent) ([]domain.HypothesisEvidenceAttribution, error) {
	if strings.TrimSpace(hypothesisID) == "" {
		return nil, fmt.Errorf("hypothesis revision is required")
	}
	if len(intents) == 0 {
		return nil, fmt.Errorf("at least one Evidence attribution is required")
	}
	byID := make(map[string]domain.Evidence, len(evidence))
	for _, item := range evidence {
		if item.ID != "" {
			byID[item.ID] = item
		}
	}
	snapshot := EvidenceSnapshotHash(evidence)
	seen := map[string]bool{}
	validatedAt := time.Now().UTC()
	out := make([]domain.HypothesisEvidenceAttribution, 0, len(intents))
	for index, intent := range intents {
		id := strings.TrimSpace(intent.EvidenceID)
		if id == "" {
			return nil, fmt.Errorf("attribution %d has no Evidence ID", index+1)
		}
		if _, ok := byID[id]; !ok {
			return nil, fmt.Errorf("attribution references unknown current Evidence ID %s", id)
		}
		if seen[id] {
			return nil, fmt.Errorf("Evidence ID %s is attributed more than once", id)
		}
		seen[id] = true
		if intent.Relation != domain.EvidenceSupports && intent.Relation != domain.EvidenceContradicts && intent.Relation != domain.EvidenceNeutral {
			return nil, fmt.Errorf("attribution for %s has invalid relation %s", id, intent.Relation)
		}
		if intent.Weight < 0 || intent.Weight > 1 {
			return nil, fmt.Errorf("attribution weight for %s must be between zero and one", id)
		}
		if strings.TrimSpace(intent.Reason) == "" {
			return nil, fmt.Errorf("attribution for %s requires a reason", id)
		}
		out = append(out, domain.HypothesisEvidenceAttribution{
			ID: fmt.Sprintf("attribution:%s:%d:%d", hypothesisID, validatedAt.UnixNano(), index), HypothesisRevisionID: hypothesisID,
			EvidenceID: id, Relation: intent.Relation, Weight: intent.Weight, Reason: strings.TrimSpace(intent.Reason),
			AttributedByTurn: turnID, EvidenceSnapshotHash: snapshot, ValidatedAt: validatedAt,
		})
	}
	return out, nil
}

func AttributedEvidenceIDs(attributions []domain.HypothesisEvidenceAttribution, relation domain.EvidenceAttributionRelation) []string {
	ids := make([]string, 0, len(attributions))
	for _, attribution := range attributions {
		if attribution.Relation == relation {
			ids = append(ids, attribution.EvidenceID)
		}
	}
	return unique(ids)
}

// Grounder performs ID-based validation over server-owned evidence. It has no
// model, ontology matching, candidate generation, or free-text causal logic.
type Grounder struct{}

func (Grounder) Validate(hypothesis domain.AgentHypothesis, evidence []domain.Evidence, input ValidationInput, previous *domain.HypothesisGrounding) (domain.HypothesisGrounding, domain.GroundingDelta) {
	byID := make(map[string]domain.Evidence, len(evidence))
	for _, item := range evidence {
		if item.ID != "" {
			byID[item.ID] = item
		}
	}
	supportIDs := existingIDs(AttributedEvidenceIDs(input.Attributions, domain.EvidenceSupports), byID)
	contradictIDs := existingIDs(AttributedEvidenceIDs(input.Attributions, domain.EvidenceContradicts), byID)
	neutralIDs := existingIDs(AttributedEvidenceIDs(input.Attributions, domain.EvidenceNeutral), byID)
	attributionIDs := make([]string, 0, len(input.Attributions))
	for _, attribution := range input.Attributions {
		attributionIDs = append(attributionIDs, attribution.ID)
	}
	sourceSupport := map[string]float64{}
	currentSupport := 0
	for _, id := range supportIDs {
		item := byID[id]
		source := canonicalSource(item.Source)
		if strength := evidenceStrength(item); strength > sourceSupport[source] {
			sourceSupport[source] = strength
		}
		if inWindow(item, input.WindowStart, input.WindowEnd) {
			currentSupport++
		}
	}
	supportValues := make([]float64, 0, len(sourceSupport))
	for _, value := range sourceSupport {
		supportValues = append(supportValues, value)
	}
	support := boundedIndependentSupport(supportValues)
	contradictionRatio := 0.0
	if total := len(supportIDs) + len(contradictIDs); total > 0 {
		contradictionRatio = float64(len(contradictIDs)) / float64(total)
	}
	evidenceNeedCoverage := ratio(len(intersection(nonEmpty(hypothesis.EvidenceNeeds), nonEmpty(input.FulfilledEvidenceNeeds))), len(nonEmpty(hypothesis.EvidenceNeeds)))
	causalCoverage := causalPathCoverage(input.ExpectedCausalNodeIDs, supportIDs, byID)
	targetCoverage := targetCoverage(input.TargetScopeDecisions, len(hypothesis.TargetRefs))
	temporalCoverage := ratio(currentSupport, len(supportIDs))
	coverage := domain.GroundingCoverage{EvidenceNeedCoverage: evidenceNeedCoverage, CausalPathCoverage: causalCoverage, TargetScopeCoverage: targetCoverage, TemporalCoverage: temporalCoverage}
	level := domain.GroundingUnknown
	switch {
	case len(contradictIDs) > 0 && (len(supportIDs) == 0 || contradictionRatio > .5):
		level = domain.GroundingRefuted
	case len(supportIDs) > 0 && evidenceNeedCoverage >= 1 && targetCoverage >= 1 && temporalCoverage >= .8 && (!input.CausalClaim || causalCoverage >= 1):
		level = domain.GroundingSupported
	case len(supportIDs) > 0 || len(contradictIDs) > 0:
		level = domain.GroundingPartial
	}
	validatedAt := time.Now().UTC()
	grounding := domain.HypothesisGrounding{
		ID: fmt.Sprintf("grounding:%s:%d", hypothesis.ID, validatedAt.UnixNano()), HypothesisRevisionID: hypothesis.ID,
		Level: level, Evidence: domain.GroundingEvidence{SupportingEvidenceIDs: supportIDs, ContradictingEvidenceIDs: contradictIDs, NeutralEvidenceIDs: neutralIDs, AttributionIDs: unique(attributionIDs), IndependentSourceCount: len(sourceSupport), EvidenceSupport: support, ContradictionRatio: contradictionRatio},
		Coverage: coverage, MissingObservations: nonEmpty(input.MissingObservations), ValidatedAt: validatedAt,
		EvidenceSnapshotHash: EvidenceSnapshotHash(evidence), CausalCoverageApplicable: input.CausalClaim,
	}
	delta := domain.GroundingDelta{HypothesisRevisionID: hypothesis.ID, CurrentLevel: level, CurrentCoverage: coverage, NewSupportingEvidenceIDs: supportIDs, NewContradictingEvidenceIDs: contradictIDs, EvidenceChange: append(append([]string{}, supportIDs...), contradictIDs...), ConflictDetected: len(supportIDs) > 0 && len(contradictIDs) > 0, SuggestedRevisionNeed: level == domain.GroundingRefuted, OccurredAt: validatedAt}
	if previous != nil {
		delta.PreviousLevel = previous.Level
		delta.PreviousCoverage = previous.Coverage
		delta.NewSupportingEvidenceIDs = difference(supportIDs, previous.Evidence.SupportingEvidenceIDs)
		delta.NewContradictingEvidenceIDs = difference(contradictIDs, previous.Evidence.ContradictingEvidenceIDs)
		delta.EvidenceChange = append(append([]string{}, delta.NewSupportingEvidenceIDs...), delta.NewContradictingEvidenceIDs...)
	}
	return grounding, delta
}

func InvalidateStaleHypotheses(hypotheses []domain.AgentHypothesis, snapshot string) []domain.AgentHypothesis {
	out := append([]domain.AgentHypothesis(nil), hypotheses...)
	for index := range out {
		if out[index].Status == domain.HypothesisSupported && out[index].LastValidatedSnapshotHash != snapshot {
			out[index].Status = domain.HypothesisInvestigating
		}
	}
	return out
}

func CommitBelief(hypothesis domain.AgentHypothesis, delta domain.BeliefDelta) (domain.AgentHypothesis, error) {
	if delta.HypothesisRevisionID != hypothesis.ID {
		return hypothesis, fmt.Errorf("belief delta references a different hypothesis revision")
	}
	if delta.NewConfidence < 0 || delta.NewConfidence > 1 {
		return hypothesis, fmt.Errorf("model confidence must be between zero and one")
	}
	if delta.PreviousConfidence != hypothesis.ModelConfidence {
		return hypothesis, fmt.Errorf("belief delta previous confidence does not match the current hypothesis revision")
	}
	if delta.NewConfidence == delta.PreviousConfidence {
		return hypothesis, fmt.Errorf("belief delta must change subjective confidence")
	}
	switch delta.Direction {
	case domain.BeliefIncrease:
		if delta.NewConfidence <= delta.PreviousConfidence {
			return hypothesis, fmt.Errorf("INCREASE direction requires higher confidence")
		}
	case domain.BeliefDecrease:
		if delta.NewConfidence >= delta.PreviousConfidence {
			return hypothesis, fmt.Errorf("DECREASE direction requires lower confidence")
		}
	default:
		return hypothesis, fmt.Errorf("belief direction must be INCREASE or DECREASE")
	}
	if delta.RevisionRequired {
		return hypothesis, fmt.Errorf("statement, mechanism, target, or falsification changes require a new revision")
	}
	hypothesis.ModelConfidence = delta.NewConfidence
	return hypothesis, nil
}

func RecoveryAllowed(diagnosis *domain.AgentDiagnosis, selected domain.AgentHypothesis, grounding domain.HypothesisGrounding, all []domain.HypothesisGrounding, execution domain.ExecutionSnapshot, kubernetesSourcePresent bool, supportSeparation float64, acceptedValidatedHypotheses int) domain.RecoveryEligibility {
	reasons := []string{}
	if diagnosis == nil || diagnosis.Provisional {
		reasons = append(reasons, "diagnosis_not_final")
	}
	if grounding.Level != domain.GroundingSupported {
		reasons = append(reasons, "grounding_not_supported")
	}
	if grounding.Evidence.EvidenceSupport < .65 {
		reasons = append(reasons, "evidence_support_below_threshold")
	}
	if grounding.Evidence.ContradictionRatio > .10 {
		reasons = append(reasons, "contradiction_ratio_above_threshold")
	}
	if grounding.Coverage.EvidenceNeedCoverage < .80 || grounding.Coverage.TargetScopeCoverage != 1 || grounding.Coverage.TemporalCoverage < .80 {
		reasons = append(reasons, "grounding_coverage_incomplete")
	}
	if grounding.CausalCoverageApplicable && grounding.Coverage.CausalPathCoverage != 1 {
		reasons = append(reasons, "causal_coverage_incomplete")
	}
	if grounding.Evidence.IndependentSourceCount < 2 || !kubernetesSourcePresent {
		reasons = append(reasons, "independent_current_sources_incomplete")
	}
	if supportSeparation < .15 {
		reasons = append(reasons, "support_separation_below_threshold")
	}
	if acceptedValidatedHypotheses < 2 || len(all) < 2 {
		reasons = append(reasons, "insufficient_competing_hypothesis_validation")
	}
	if diagnosis != nil && (diagnosis.HypothesisRevisionID != selected.ID || diagnosis.EvidenceSnapshotHash != grounding.EvidenceSnapshotHash || diagnosis.ExecutionSnapshot != execution || selected.LastValidatedSnapshotHash != grounding.EvidenceSnapshotHash) {
		reasons = append(reasons, "snapshot_or_revision_mismatch")
	}
	return domain.RecoveryEligibility{Allowed: len(reasons) == 0, ReasonCodes: unique(reasons)}
}

func ReflectionCost(trigger domain.ReflectionTrigger) int {
	switch trigger {
	case domain.ReflectionToolFailure, domain.ReflectionConstraintFailure:
		return 1
	case domain.ReflectionRecoveryFailure, domain.ReflectionVerificationFail:
		return 3
	default:
		return 2
	}
}

func NewTermination(reason domain.TerminationReason, turnID, hypothesisID, evidenceSnapshot string, execution *domain.ExecutionSnapshot, gaps []string, budget domain.BrainBudgetState) (domain.TerminationEvent, error) {
	if reason == "" {
		return domain.TerminationEvent{}, fmt.Errorf("termination reason is required")
	}
	remaining := domain.BrainBudgetUsage{
		Turns:                 max(0, budget.Limits.MaxTurns-budget.Usage.Turns),
		ActiveHypotheses:      max(0, budget.Limits.MaxActiveHypotheses-budget.Usage.ActiveHypotheses),
		HypothesisBranches:    max(0, budget.Limits.MaxHypothesisBranches-budget.Usage.HypothesisBranches),
		ToolCalls:             max(0, budget.Limits.MaxToolCalls-budget.Usage.ToolCalls),
		StructuredCorrections: max(0, budget.Limits.MaxStructuredCorrections-budget.Usage.StructuredCorrections),
		ReflectionCostUnits:   max(0, budget.Limits.MaxReflectionCostUnits-budget.Usage.ReflectionCostUnits),
	}
	return domain.TerminationEvent{Reason: reason, TriggerTurnID: turnID, FinalHypothesisRevisionID: hypothesisID, EvidenceSnapshotHash: evidenceSnapshot, ExecutionSnapshot: execution, UnresolvedGaps: nonEmpty(gaps), RemainingBudget: remaining, OccurredAt: time.Now().UTC()}, nil
}

func existingIDs(ids []string, evidence map[string]domain.Evidence) []string {
	out := []string{}
	for _, id := range nonEmpty(ids) {
		if _, ok := evidence[id]; ok {
			out = append(out, id)
		}
	}
	return unique(out)
}

func evidenceStrength(item domain.Evidence) float64 {
	value := item.QualityScore
	if value <= 0 {
		value = item.Confidence
	}
	if value <= 0 {
		value = 1
	}
	if value > 1 {
		return 1
	}
	return value
}

func boundedIndependentSupport(values []float64) float64 {
	remaining := 1.0
	for _, value := range values {
		remaining *= 1 - value
	}
	return 1 - remaining
}

func inWindow(item domain.Evidence, start, end time.Time) bool {
	if start.IsZero() && end.IsZero() {
		return true
	}
	observed := item.ObservedAt
	if observed.IsZero() {
		observed = item.Timestamp
	}
	if observed.IsZero() {
		observed = item.CollectedAt
	}
	return (start.IsZero() || !observed.Before(start)) && (end.IsZero() || !observed.After(end))
}

func causalPathCoverage(expected, supportIDs []string, evidence map[string]domain.Evidence) float64 {
	expected = nonEmpty(expected)
	if len(expected) == 0 {
		return 0
	}
	observed := map[string]bool{}
	for _, id := range supportIDs {
		for _, node := range evidence[id].CausalNodeIDs {
			observed[node] = true
		}
	}
	count := 0
	for _, node := range expected {
		if observed[node] {
			count++
		}
	}
	return ratio(count, len(expected))
}

func targetCoverage(decisions []domain.ResourceScopeDecision, targets int) float64 {
	if targets == 0 {
		return 0
	}
	allowed := 0
	for _, decision := range decisions {
		if decision.Allowed {
			allowed++
		}
	}
	return ratio(allowed, targets)
}

func ratio(numerator, denominator int) float64 {
	if denominator == 0 {
		return 0
	}
	value := float64(numerator) / float64(denominator)
	if value > 1 {
		return 1
	}
	return value
}

func canonicalSource(source string) string {
	source = strings.ToLower(strings.TrimSpace(source))
	switch source {
	case "prometheus", "metric", "metrics":
		return "metric"
	case "loki", "log", "logs":
		return "log"
	case "jaeger", "tempo", "trace", "traces":
		return "trace"
	case "kubernetes", "k8s":
		return "kubernetes"
	default:
		return source
	}
}

func nonEmpty(values []string) []string {
	out := []string{}
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			out = append(out, value)
		}
	}
	return unique(out)
}

func unique(values []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, value := range values {
		if value != "" && !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	sort.Strings(out)
	return out
}

func appendUniqueCategory(values []domain.BrainToolCategory, value domain.BrainToolCategory) []domain.BrainToolCategory {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func intersection(left, right []string) []string {
	rightSet := map[string]bool{}
	for _, value := range right {
		rightSet[value] = true
	}
	out := []string{}
	for _, value := range left {
		if rightSet[value] {
			out = append(out, value)
		}
	}
	return unique(out)
}

func difference(left, right []string) []string {
	rightSet := map[string]bool{}
	for _, value := range right {
		rightSet[value] = true
	}
	out := []string{}
	for _, value := range left {
		if !rightSet[value] {
			out = append(out, value)
		}
	}
	return unique(out)
}

func resourceKey(ref domain.ResourceRef) string {
	return strings.Join([]string{ref.Namespace, ref.Kind, ref.Service, ref.Resource}, "|")
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
