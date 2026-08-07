package agent

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/kubepilot-aiops/kubepilot/internal/domain"
	"github.com/kubepilot-aiops/kubepilot/reasoning"
)

// cognitiveModeForMethod identifies the frozen deterministic-diagnosis
// strategies. Direct, RAG, and ReAct retain their existing execution paths.
func cognitiveModeForMethod(method string) (cognitiveDiagnosisMode, bool) {
	// An empty method is a legacy low-level call path whose established
	// behavior is the constrained ReAct runtime. Production ingress persists a
	// canonical method before it reaches this graph.
	if strings.TrimSpace(method) == "" {
		return cognitiveDiagnosisMode{}, false
	}
	normalized, ok := domain.NormalizeDiagnosisMethod(method)
	if !ok {
		return cognitiveDiagnosisMode{}, false
	}
	switch normalized {
	case domain.DiagnosisMethodRuleOnly:
		return cognitiveDiagnosisMode{RuleOnly: true}, true
	case domain.DiagnosisMethodEvidence:
		return cognitiveDiagnosisMode{}, true
	case domain.DiagnosisMethodCognitive:
		return cognitiveDiagnosisMode{Cognitive: true}, true
	case domain.DiagnosisMethodActive:
		return cognitiveDiagnosisMode{Cognitive: true, Active: true}, true
	default:
		return cognitiveDiagnosisMode{}, false
	}
}

func (r *AgentRegistry) initializeCognitiveDiagnosis(state *WorkflowState) error {
	if state == nil || state.Incident == nil {
		return fmt.Errorf("workflow state and incident are required")
	}
	if state.DiagnosisRuntime != nil {
		return nil
	}
	mode, ok := cognitiveModeForMethod(state.Incident.DiagnosisMethod)
	if !ok {
		return nil
	}
	plan := serverInvestigationPlan(state.Incident)
	maxRounds := 1
	if mode.Active {
		maxRounds = 2
	}
	plan.RoundLimit = maxRounds
	state.DiagnosisRuntime = &CognitiveDiagnosisState{
		Method:                  state.Incident.DiagnosisMethod,
		RuleOnly:                mode.RuleOnly,
		Cognitive:               mode.Cognitive,
		Active:                  mode.Active,
		Round:                   1,
		MaxRounds:               maxRounds,
		Plan:                    plan,
		PendingRequests:         planRequests(plan, state.Incident),
		SeenRequestFingerprints: map[string]bool{},
		Evidence:                normalizeCollectedEvidence(state.Incident, state.Incident.Evidence),
		Investigation: &domain.Investigation{
			Architecture: diagnosisArchitecture(state.Incident.DiagnosisMethod),
			Plan:         plan,
			StartedAt:    time.Now().UTC(),
		},
	}
	return nil
}

// cognitiveIntentNode runs the Planner capability only for Active Diagnosis.
// The plan itself remains server-compiled and all four core sources are still
// queried in round one.
func (r *AgentRegistry) cognitiveIntentNode(ctx context.Context, state *WorkflowState) error {
	if err := r.initializeCognitiveDiagnosis(state); err != nil {
		return err
	}
	runtime := state.DiagnosisRuntime
	if runtime == nil || !runtime.Cognitive || !runtime.Active || len(runtime.Investigation.CognitiveReasoning) > 0 {
		return nil
	}
	record, expansions, usage, err := r.runCognitiveReasoning(ctx, state.Incident, 0, nil, nil, nil, true)
	runtime.Investigation.ModelUsage = append(runtime.Investigation.ModelUsage, usage)
	if err != nil {
		runtime.Investigation.CognitiveReasoning = append(runtime.Investigation.CognitiveReasoning, domain.CognitiveReasoning{
			Round: 0, OccurredAt: time.Now().UTC(), RejectedReasons: []string{"cognitive_planner_unavailable"},
		})
		return nil
	}
	runtime.Investigation.CognitiveReasoning = append(runtime.Investigation.CognitiveReasoning, record)
	runtime.Investigation.ExpansionRequests = append(runtime.Investigation.ExpansionRequests, expansions...)
	return nil
}

// queryCompilerNode accepts only server-validated InvestigationPolicy values;
// it never accepts free-form PromQL, LogQL, targets, or namespace overrides.
func (r *AgentRegistry) queryCompilerNode(state *WorkflowState) error {
	if err := r.initializeCognitiveDiagnosis(state); err != nil {
		return err
	}
	runtime := state.DiagnosisRuntime
	if runtime == nil || runtime.Completed || len(runtime.PendingRequests) > 0 {
		return nil
	}
	requests := evidenceRequestsForPolicies(state.Incident, runtime.PendingPolicies, runtime.Evidence)
	if len(requests) == 0 {
		runtime.Completed = true
		runtime.StopReason = "no server-actionable discriminating observation was proposed"
		runtime.Arbitration.NeedsMoreEvidence = true
		runtime.Arbitration.Reason = runtime.StopReason
		return nil
	}
	runtime.PendingRequests = requests
	return nil
}

func (r *AgentRegistry) evidenceCollectionNode(ctx context.Context, state *WorkflowState, deps constrainedToolDeps) error {
	if err := r.initializeCognitiveDiagnosis(state); err != nil {
		return err
	}
	runtime := state.DiagnosisRuntime
	if runtime == nil || runtime.Completed {
		return nil
	}
	unique := uniqueEvidenceRequests(runtime.PendingRequests, runtime.SeenRequestFingerprints)
	runtime.PendingRequests = nil
	if len(unique) == 0 {
		runtime.Completed = true
		runtime.StopReason = "duplicate or zero-value evidence request"
		runtime.Arbitration.NeedsMoreEvidence = true
		runtime.Arbitration.Reason = runtime.StopReason
		return nil
	}
	before := len(runtime.Evidence)
	collected, infrastructure := collectEvidenceRequests(ctx, state.Incident, deps.Collectors, unique, allowedEvidenceTargets(state.Incident, runtime.Evidence))
	if state.Incident.DiagnosisLedger == nil {
		state.Incident.DiagnosisLedger = &state.DiagnosisLedger
	}
	state.DiagnosisLedger.InfrastructureErrors = append(state.DiagnosisLedger.InfrastructureErrors, infrastructure...)
	runtime.Evidence = mergeEvidence(runtime.Evidence, collected)
	runtime.Investigation.Findings = append(runtime.Investigation.Findings, serverWorkerFindings(unique, collected, infrastructure, runtime.Round)...)
	if runtime.Round > 1 && runtime.PolicyBaseline != nil {
		runtime.PolicyBaseline.NewEvidence = len(runtime.Evidence) > before
	}
	if runtime.Round > 1 && len(runtime.Evidence) == before {
		resolvePendingPolicyOutcomes(runtime, false, runtime.Arbitration)
		runtime.Completed = true
		runtime.StopReason = "supplementary collection produced no new evidence"
		runtime.Arbitration.NeedsMoreEvidence = true
		runtime.Arbitration.Reason = runtime.StopReason
	}
	return nil
}

func (r *AgentRegistry) signalAssertionBuilderNode(state *WorkflowState, deps constrainedToolDeps) error {
	runtime := state.DiagnosisRuntime
	if runtime == nil || runtime.Completed {
		return nil
	}
	ranked, err := deps.Reasoning.RankEvidence(state.Incident, runtime.Evidence)
	if err != nil {
		sourceSummary := evidenceSourceSummary(runtime.Evidence)
		if len(state.DiagnosisLedger.InfrastructureErrors) > 0 {
			return fmt.Errorf("%w; collected sources: %s; collection failures: %s", err, sourceSummary, strings.Join(state.DiagnosisLedger.InfrastructureErrors, "; "))
		}
		return fmt.Errorf("%w; collected sources: %s", err, sourceSummary)
	}
	// The deterministic runtime consumes every normalized observation. Only the
	// Eino model boundary uses RankedEvidence.Evidence, whose projection is
	// bounded by the context contract. Coupling the diagnosis graph to that
	// presentation budget previously discarded low-ranked but decisive signals
	// such as a memory trend or a workload event.
	runtime.Evidence = ranked.RuntimeEvidence
	if len(runtime.Evidence) == 0 {
		runtime.Evidence = ranked.Evidence
	}
	runtime.Assertions = reasoning.BuildStateAssertions(state.Incident, runtime.Evidence, runtime.Assertions, time.Now().UTC())
	runtime.Investigation.Signals = collectSignals(runtime.Evidence)
	runtime.Investigation.Assertions = append([]domain.StateAssertion(nil), runtime.Assertions...)
	state.RankedEvidence = append([]domain.Evidence(nil), runtime.Evidence...)
	state.StateAssertions = append([]domain.StateAssertion(nil), runtime.Assertions...)
	return nil
}

func evidenceSourceSummary(items []domain.Evidence) string {
	counts := map[string]int{}
	for _, item := range items {
		counts[string(item.Source)]++
	}
	if len(counts) == 0 {
		return "none"
	}
	sources := make([]string, 0, len(counts))
	for source := range counts {
		sources = append(sources, source)
	}
	sort.Strings(sources)
	parts := make([]string, 0, len(sources))
	for _, source := range sources {
		parts = append(parts, fmt.Sprintf("%s=%d", source, counts[source]))
	}
	return strings.Join(parts, ",")
}

func (r *AgentRegistry) candidateGenerationNode(ctx context.Context, state *WorkflowState, deps constrainedToolDeps) error {
	runtime := state.DiagnosisRuntime
	if runtime == nil || runtime.Completed {
		return nil
	}
	patterns, annotatedEvidence, err := loadRuntimeCausalPatterns(ctx, state.Incident, runtime.Evidence, deps)
	if err != nil {
		return err
	}
	runtime.CausalPatterns = patterns
	runtime.Evidence = annotatedEvidence
	runtime.Drafts = reasoning.GenerateDeterministicCandidates(state.Incident, runtime.Assertions, runtime.Evidence, patterns)
	if runtime.UnresolvedCandidate != nil {
		runtime.Drafts = append(runtime.Drafts, *runtime.UnresolvedCandidate)
	}
	verifiable := make([]domain.HypothesisDraft, 0, len(runtime.Drafts))
	for _, draft := range runtime.Drafts {
		if draft.ID != "unresolved-mechanism" {
			verifiable = append(verifiable, draft)
		}
	}
	verified, err := deps.Reasoning.VerifyHypotheses(verifiable, runtime.Evidence, nil, patterns, runtime.Assertions)
	if err != nil {
		return err
	}
	runtime.Verified = verified
	runtime.Investigation.Candidates = append([]domain.HypothesisDraft(nil), runtime.Drafts...)
	runtime.Investigation.Verified = append([]domain.VerifiedHypothesis(nil), runtime.Verified...)
	state.HypothesisDrafts = append([]domain.HypothesisDraft(nil), runtime.Drafts...)
	state.VerifiedHypotheses = append([]domain.VerifiedHypothesis(nil), runtime.Verified...)
	state.CausalPatterns = append([]domain.CausalPattern(nil), patterns...)
	return nil
}

// loadRuntimeCausalPatterns only exposes active server-owned patterns. Their
// deterministic match is based on normalized current evidence and is shared
// by candidate generation and coverage verification; no model output can add
// a causal node or edge.
func loadRuntimeCausalPatterns(ctx context.Context, incident *domain.Incident, evidence []domain.Evidence, deps constrainedToolDeps) ([]domain.CausalPattern, []domain.Evidence, error) {
	if deps.Knowledge == nil {
		return nil, evidence, nil
	}
	patterns, err := deps.Knowledge.ListCausalPatterns(ctx, "active")
	if err != nil {
		return nil, nil, fmt.Errorf("load active causal patterns: %w", err)
	}
	// First project typed, anomalous server signals onto all active canonical
	// nodes. Pattern selection is then based on those node IDs—not on text in
	// evidence summaries or incident terms—so a common service name or symptom
	// can never activate an unrelated causal path.
	annotated := deps.Reasoning.AnnotateCausalNodes(evidence, patterns)
	features := deps.Reasoning.BuildFeatures(incident, annotated)
	return deps.Reasoning.MatchCausalPatterns(features, annotated, patterns), annotated, nil
}

// cognitiveReasoningNode uses one Eino-managed cognitive component. Its
// structured output is validated before it is made visible to later nodes.
func (r *AgentRegistry) cognitiveReasoningNode(ctx context.Context, state *WorkflowState) error {
	runtime := state.DiagnosisRuntime
	if runtime == nil || runtime.Completed || !runtime.Cognitive {
		return nil
	}
	record, expansions, usage, err := r.runCognitiveReasoning(ctx, state.Incident, runtime.Round, runtime.Assertions, runtime.Drafts, runtime.Verified, false)
	runtime.Investigation.ModelUsage = append(runtime.Investigation.ModelUsage, usage)
	if err != nil {
		record = domain.CognitiveReasoning{Round: runtime.Round, OccurredAt: time.Now().UTC(), RejectedReasons: []string{"cognitive_runtime_unavailable"}}
	}
	runtime.Investigation.CognitiveReasoning = append(runtime.Investigation.CognitiveReasoning, record)
	if len(expansions) > 0 {
		for index := range expansions {
			if runtime.UnresolvedCandidateActive {
				expansions[index].Status = "rejected_limit_reached"
				continue
			}
			expansions[index].Status = "activated_non_actionable"
			runtime.UnresolvedCandidateActive = true
			runtime.UnresolvedCandidate = &domain.HypothesisDraft{
				ID: "unresolved-mechanism", Category: "unknown", Variant: "unresolved_mechanism",
				Cause:   "unresolved mechanism requiring additional observation and human review",
				Service: state.Incident.Service, Resource: state.Incident.Resource,
			}
			// Make the bounded, non-actionable candidate visible immediately in
			// the investigation audit. It is deliberately excluded from the
			// verified ledger, so it cannot become a recovery target.
			runtime.Drafts = append(runtime.Drafts, *runtime.UnresolvedCandidate)
			state.HypothesisDrafts = append(state.HypothesisDrafts, *runtime.UnresolvedCandidate)
		}
		runtime.Investigation.ExpansionRequests = append(runtime.Investigation.ExpansionRequests, expansions...)
	}
	runtime.PendingPolicies = record.InvestigationPolicies
	return nil
}

func (r *AgentRegistry) causalFalsificationNode(state *WorkflowState) error {
	runtime := state.DiagnosisRuntime
	if runtime == nil || runtime.Completed {
		return nil
	}
	// Rule-only is intentionally the lower-bound ontology baseline. It shares
	// server-derived evidence and candidates with Evidence-only, but does not
	// run the causal/falsification services or create those audit artifacts.
	if runtime.RuleOnly {
		return nil
	}
	runtime.Investigation.Falsification = deterministicFalsification(runtime.Drafts, runtime.Assertions)
	runtime.Investigation.Pairwise = deterministicPairwise(runtime.Verified, runtime.Assertions)
	return nil
}

// objectiveArbitrationNode is entirely deterministic. Cognitive preference is
// ordinal-only: it changes the presentation order of an already-near tie, not
// any score, margin, gate, or recovery permission.
func (r *AgentRegistry) objectiveArbitrationNode(state *WorkflowState) error {
	runtime := state.DiagnosisRuntime
	if runtime == nil || runtime.Completed {
		return nil
	}
	runtime.Arbitration = arbitrateHypotheses(runtime.Verified, runtime.Evidence)
	for _, record := range runtime.Investigation.CognitiveReasoning {
		if record.Round == runtime.Round {
			applyTieBreakingPreference(&runtime.Arbitration, runtime.Verified, record.TieBreakingPreferences)
		}
	}
	if runtime.PolicyBaseline != nil && runtime.Round > 1 {
		resolvePendingPolicyOutcomes(runtime, runtime.PolicyBaseline.NewEvidence, runtime.Arbitration)
	}
	if runtime.Arbitration.Accepted || !runtime.Active || runtime.Round >= runtime.MaxRounds {
		markPoliciesStopped(runtime)
		runtime.Completed = true
		return nil
	}
	// A failed initial arbitration can legitimately have no candidate pair for
	// the LLM Investigator to compare. If topology has already established a
	// one-hop dependency and the incident exposes downstream failure signals,
	// the server can still make the bounded, discriminating endpoint request.
	// This preserves the cognitive boundary: no model output chooses a target,
	// predicate, candidate, or query text.
	if requests := serverDependencyExplorationRequests(state.Incident, runtime.Evidence, runtime.Assertions); len(requests) > 0 {
		runtime.PendingRequests = requests
		runtime.PolicyBaseline = &policyDecisionSnapshot{
			TopID: runtime.Arbitration.SelectedHypothesisID, Margin: runtime.Arbitration.ScoreMargin,
			Accepted: runtime.Arbitration.Accepted, Entropy: candidateEntropy(runtime.Verified),
		}
		runtime.Round++
		return nil
	}
	policies, evaluated := evaluateInvestigationPolicies(runtime.PendingPolicies, runtime.Verified, runtime.Assertions)
	applyPolicyEvaluation(runtime, evaluated)
	if len(policies) == 0 {
		runtime.Completed = true
		runtime.StopReason = "no positive diagnostic value for a supplemental observation"
		runtime.Arbitration.NeedsMoreEvidence = true
		runtime.Arbitration.Reason = runtime.StopReason
		return nil
	}
	runtime.PendingPolicies = policies
	runtime.PolicyBaseline = &policyDecisionSnapshot{
		TopID: runtime.Arbitration.SelectedHypothesisID, Margin: runtime.Arbitration.ScoreMargin,
		Accepted: runtime.Arbitration.Accepted, Entropy: candidateEntropy(runtime.Verified),
	}
	runtime.Round++
	return nil
}

// valueQualifiedPolicies is retained for callers and tests that only need the
// server-accepted proposals. Audit-aware graph nodes use the paired evaluator.
func valueQualifiedPolicies(policies []domain.InvestigationPolicy, verified []domain.VerifiedHypothesis, assertions []domain.StateAssertion) []domain.InvestigationPolicy {
	accepted, _ := evaluateInvestigationPolicies(policies, verified, assertions)
	return accepted
}

func evaluateInvestigationPolicies(policies []domain.InvestigationPolicy, verified []domain.VerifiedHypothesis, assertions []domain.StateAssertion) ([]domain.InvestigationPolicy, []domain.InvestigationPolicy) {
	scores := map[string]float64{}
	categories := map[string]string{}
	for _, candidate := range verified {
		scores[candidate.Draft.ID] = candidate.ObjectiveScore
		categories[candidate.Draft.ID] = candidate.Draft.Category
	}
	abnormal := map[string]bool{}
	for _, assertion := range assertions {
		abnormal[assertion.Property] = assertion.Status == domain.StateAssertionActive && assertion.State == "abnormal"
	}
	evaluated := make([]domain.InvestigationPolicy, 0, len(policies))
	accepted := make([]domain.InvestigationPolicy, 0, len(policies))
	for _, policy := range policies {
		if len(policy.CandidateIDs) != 2 || categories[policy.CandidateIDs[0]] == "" || categories[policy.CandidateIDs[1]] == "" || abnormal[policy.ObservationKind] {
			policy.Status = "rejected_already_observed_or_invalid_pair"
			evaluated = append(evaluated, policy)
			continue
		}
		if !observationRequiredByCandidatePair(policy.ObservationKind, categories[policy.CandidateIDs[0]], categories[policy.CandidateIDs[1]]) {
			policy.Status = "rejected_no_required_unobserved_assertion"
			evaluated = append(evaluated, policy)
			continue
		}
		gap := abs(scores[policy.CandidateIDs[0]] - scores[policy.CandidateIDs[1]])
		policy.ExpectedEntropyReduction = .50 * (1 - gap)
		policy.DecisionImpact = 0
		if gap <= .15 || (scores[policy.CandidateIDs[0]] >= .65) != (scores[policy.CandidateIDs[1]] >= .65) {
			policy.DecisionImpact = 1
		}
		policy.DiagnosticValue = policy.ExpectedEntropyReduction * policy.DecisionImpact
		if policy.DiagnosticValue < .05 {
			policy.Status = "rejected_low_diagnostic_value"
			evaluated = append(evaluated, policy)
			continue
		}
		policy.Status = "accepted"
		accepted = append(accepted, policy)
		evaluated = append(evaluated, policy)
	}
	sort.SliceStable(accepted, func(i, j int) bool { return accepted[i].DiagnosticValue > accepted[j].DiagnosticValue })
	if len(accepted) > 2 {
		for _, policy := range accepted[2:] {
			for index := range evaluated {
				if evaluated[index].CandidateIDs[0] == policy.CandidateIDs[0] && evaluated[index].CandidateIDs[1] == policy.CandidateIDs[1] && evaluated[index].ObservationKind == policy.ObservationKind {
					evaluated[index].Status = "rejected_round_request_limit"
				}
			}
		}
		accepted = accepted[:2]
	}
	return accepted, evaluated
}

func observationRequiredByCandidatePair(observation, leftCategory, rightCategory string) bool {
	for _, category := range []string{leftCategory, rightCategory} {
		for _, expected := range expectedObservationPropertiesForCategory(category) {
			if observation == expected {
				return true
			}
		}
	}
	return false
}

func applyPolicyEvaluation(runtime *CognitiveDiagnosisState, policies []domain.InvestigationPolicy) {
	if runtime == nil || runtime.Investigation == nil || len(policies) == 0 {
		return
	}
	for index := len(runtime.Investigation.CognitiveReasoning) - 1; index >= 0; index-- {
		if runtime.Investigation.CognitiveReasoning[index].Round == runtime.Round {
			runtime.Investigation.CognitiveReasoning[index].InvestigationPolicies = policies
			return
		}
	}
}

func markPoliciesStopped(runtime *CognitiveDiagnosisState) {
	if runtime == nil || len(runtime.PendingPolicies) == 0 {
		return
	}
	_, evaluated := evaluateInvestigationPolicies(runtime.PendingPolicies, runtime.Verified, runtime.Assertions)
	for index := range evaluated {
		if evaluated[index].Status == "accepted" {
			evaluated[index].Status = "rejected_stop_condition"
		}
	}
	applyPolicyEvaluation(runtime, evaluated)
}

func resolvePendingPolicyOutcomes(runtime *CognitiveDiagnosisState, newEvidence bool, arbitration domain.ArbitrationResult) {
	if runtime == nil || runtime.PolicyBaseline == nil {
		return
	}
	baseline := runtime.PolicyBaseline
	baseline.NewEvidence = newEvidence
	changed := arbitration.SelectedHypothesisID != baseline.TopID || arbitration.Accepted != baseline.Accepted || abs(arbitration.ScoreMargin-baseline.Margin) > .01 || abs(candidateEntropy(runtime.Verified)-baseline.Entropy) > .01
	for index := range runtime.Investigation.CognitiveReasoning {
		for policyIndex := range runtime.Investigation.CognitiveReasoning[index].InvestigationPolicies {
			policy := &runtime.Investigation.CognitiveReasoning[index].InvestigationPolicies[policyIndex]
			if policy.Status != "accepted" {
				continue
			}
			switch {
			case !newEvidence:
				policy.Status = "ineffective_no_new_evidence"
			case changed:
				policy.Status = "useful"
			default:
				policy.Status = "ineffective_no_decision_change"
			}
		}
	}
	runtime.PolicyBaseline = nil
}

func candidateEntropy(verified []domain.VerifiedHypothesis) float64 {
	if len(verified) < 2 {
		return 0
	}
	ordered := append([]domain.VerifiedHypothesis(nil), verified...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].ObjectiveScore > ordered[j].ObjectiveScore })
	return .50 * (1 - abs(ordered[0].ObjectiveScore-ordered[1].ObjectiveScore))
}

func cognitiveDiagnosisResult(state *WorkflowState) (DiagnosisResult, error) {
	runtime := state.DiagnosisRuntime
	if runtime == nil || runtime.Investigation == nil {
		return DiagnosisResult{}, fmt.Errorf("cognitive diagnosis runtime is not initialized")
	}
	runtime.Investigation.Arbitration = &runtime.Arbitration
	runtime.Investigation.RecoveryPermission = recoveryPermission(runtime.Verified, runtime.Arbitration)
	runtime.Investigation.DiagnosisRounds = runtime.Round
	runtime.Investigation.CompletedAt = time.Now().UTC()
	selected := ""
	if runtime.Arbitration.Accepted {
		selected = runtime.Arbitration.SelectedHypothesisID
	}
	return DiagnosisResult{
		Method: state.Incident.DiagnosisMethod, Hypotheses: runtime.Drafts, Verified: runtime.Verified,
		Assertions: runtime.Assertions, SelectedHypothesisID: selected, Evidence: runtime.Evidence, CausalPatterns: runtime.CausalPatterns,
		Investigation: runtime.Investigation,
	}, nil
}
