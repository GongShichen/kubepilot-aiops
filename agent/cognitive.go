package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/kubepilot-aiops/kubepilot/internal/domain"
)

var allowedCognitivePredicates = map[string]bool{
	"precedes": true, "co_occurs": true, "absent": true, "same_scope": true, "contradicts": true,
}

var allowedObservationKinds = map[string]bool{
	"cpu_pressure": true, "cpu_throttling": true, "memory_pressure": true, "memory_growth": true,
	"request_latency": true, "application_errors": true, "pod_restarts": true,
	"connection_pressure": true, "dependency_availability": true,
	"network_connectivity": true, "workload_health": true,
	"recent_diff": true, "thread_dump": true, "profile": true, "trace_error": true,
}

type cognitiveResponse struct {
	Intent                 *domain.InvestigationIntent        `json:"intent,omitempty"`
	Interpretations        []domain.CognitiveInterpretation   `json:"interpretations,omitempty"`
	TieBreakingPreferences []domain.TieBreakingPreference     `json:"tie_breaking_preferences,omitempty"`
	InvestigationPolicies  []domain.InvestigationPolicy       `json:"investigation_policies,omitempty"`
	Counterarguments       []domain.CognitiveCounterargument  `json:"counterarguments,omitempty"`
	ExpansionRequests      []domain.CandidateExpansionRequest `json:"candidate_expansion_requests,omitempty"`
}

// runCognitiveReasoning is the single LLM entrypoint for the cognitive layer.
// The output is a proposal only; callers must validate it before it can affect
// ordering or trigger a server-compiled evidence request.
func (r *AgentRegistry) runCognitiveReasoning(ctx context.Context, incident *domain.Incident, round int, assertions []domain.StateAssertion, drafts []domain.HypothesisDraft, verified []domain.VerifiedHypothesis, includeIntent bool) (domain.CognitiveReasoning, []domain.CandidateExpansionRequest, domain.ModelUsageEvent, error) {
	if incident == nil {
		return domain.CognitiveReasoning{}, nil, domain.ModelUsageEvent{}, fmt.Errorf("incident is required")
	}
	payload, _ := json.Marshal(map[string]any{
		"incident":                  safeIncident(incident),
		"state_assertions":          assertions,
		"candidates":                cognitiveCandidates(drafts, verified),
		"allowed_predicates":        sortedCognitiveKeys(allowedCognitivePredicates),
		"allowed_observation_kinds": sortedCognitiveKeys(allowedObservationKinds),
		"constraints": map[string]any{
			"allow_intent":                   includeIntent,
			"maximum_interpretations":        3,
			"maximum_preferences":            1,
			"maximum_investigation_policies": 2,
			"output":                         "JSON only; all IDs must be supplied server IDs",
		},
	})
	started := time.Now()
	message, err := r.generateRole(ctx, CognitiveRuntimeName, `Return exactly one JSON object. Shape: {"intent":{"focus":["..."],"priorities":["..."]},"interpretations":[{"candidate_ids":["..."],"mechanism_labels":["..."],"supporting_assertion_ids":["..."],"reasoning_predicates":["precedes"],"required_observations":["..."]}],"tie_breaking_preferences":[{"preferred_candidate_id":"...","other_candidate_id":"...","assertion_ids":["..."],"predicates":["..."]}],"counterarguments":[{"candidate_id":"...","assertion_ids":["..."],"observation_kinds":["..."]}],"investigation_policies":[{"candidate_ids":["...","..."],"observation_kind":"...","rationale_predicates":["..."]}],"candidate_expansion_requests":[{"assertion_ids":["..."],"required_observations":["..."],"reason":"..."}]}. Omit intent when allow_intent is false.`, string(payload))
	usage := r.modelUsage(incident.ID, CognitiveRuntimeName, message, time.Since(started))
	if err != nil {
		return domain.CognitiveReasoning{}, nil, usage, err
	}
	var response cognitiveResponse
	if err = decodeModelJSON(message.Content, &response); err != nil {
		return domain.CognitiveReasoning{}, nil, usage, structuredOutputError(CognitiveRuntimeName, message, err)
	}
	record, expansions := validateCognitiveResponse(round, response, assertions, drafts, verified, includeIntent)
	return record, expansions, usage, nil
}

func cognitiveCandidates(drafts []domain.HypothesisDraft, verified []domain.VerifiedHypothesis) []map[string]any {
	verifiedByID := map[string]domain.VerifiedHypothesis{}
	for _, item := range verified {
		verifiedByID[item.Draft.ID] = item
	}
	out := make([]map[string]any, 0, len(drafts))
	for _, draft := range drafts {
		entry := map[string]any{"id": draft.ID, "category": draft.Category, "variant": draft.Variant, "cause": draft.Cause, "supporting_evidence_ids": draft.SupportingEvidenceIDs}
		if item, ok := verifiedByID[draft.ID]; ok {
			entry["objective_score"] = item.ObjectiveScore
		}
		out = append(out, entry)
	}
	return out
}

func validateCognitiveResponse(round int, response cognitiveResponse, assertions []domain.StateAssertion, drafts []domain.HypothesisDraft, verified []domain.VerifiedHypothesis, includeIntent bool) (domain.CognitiveReasoning, []domain.CandidateExpansionRequest) {
	assertionIDs := map[string]bool{}
	for _, assertion := range assertions {
		assertionIDs[assertion.ID] = true
	}
	candidateIDs := map[string]bool{}
	for _, draft := range drafts {
		candidateIDs[draft.ID] = true
	}
	scores := map[string]float64{}
	for _, item := range verified {
		scores[item.Draft.ID] = item.ObjectiveScore
	}
	record := domain.CognitiveReasoning{Round: round, OccurredAt: time.Now().UTC()}
	if includeIntent && response.Intent != nil {
		record.Intent = &domain.InvestigationIntent{Focus: filterKnownObservationKinds(response.Intent.Focus), Priorities: filterKnownObservationKinds(response.Intent.Priorities)}
	}
	for _, interpretation := range response.Interpretations {
		if validInterpretation(interpretation, assertionIDs, candidateIDs) {
			record.Interpretations = append(record.Interpretations, interpretation)
		} else {
			record.RejectedReasons = append(record.RejectedReasons, "invalid_interpretation")
		}
	}
	for _, preference := range response.TieBreakingPreferences {
		if validPreference(preference, assertionIDs, candidateIDs, scores) {
			record.TieBreakingPreferences = append(record.TieBreakingPreferences, preference)
		} else {
			record.RejectedReasons = append(record.RejectedReasons, "invalid_tie_breaking_preference")
		}
	}
	for _, counter := range response.Counterarguments {
		if candidateIDs[counter.CandidateID] && allKnown(counter.AssertionIDs, assertionIDs) && allObservationKinds(counter.ObservationKinds) {
			record.Counterarguments = append(record.Counterarguments, counter)
		} else {
			record.RejectedReasons = append(record.RejectedReasons, "invalid_counterargument")
		}
	}
	for _, policy := range response.InvestigationPolicies {
		if len(policy.CandidateIDs) == 2 && allKnown(policy.CandidateIDs, candidateIDs) && allowedObservationKinds[policy.ObservationKind] && allPredicates(policy.RationalePredicates) {
			record.InvestigationPolicies = append(record.InvestigationPolicies, policy)
		} else {
			record.RejectedReasons = append(record.RejectedReasons, "invalid_investigation_policy")
		}
	}
	// Models often express an investigation strategy as an interpretation for
	// one candidate plus a list of missing observations, rather than repeating
	// the same IDs in the stricter policy shape. The runtime can safely compile
	// that proposal into a candidate pair by selecting the nearest *existing*
	// objective competitor. It never creates a candidate, query, target, or
	// predicate: normal policy validation and value gating still apply below.
	record.InvestigationPolicies = append(record.InvestigationPolicies, policiesFromInterpretations(record.Interpretations, verified)...)
	record.InvestigationPolicies = uniqueInvestigationPolicies(record.InvestigationPolicies)
	var expansions []domain.CandidateExpansionRequest
	for _, request := range response.ExpansionRequests {
		if allKnown(request.AssertionIDs, assertionIDs) && len(request.RequiredObservations) > 0 && allObservationKinds(request.RequiredObservations) {
			request.Status = "accepted_for_server_review"
			expansions = append(expansions, request)
		} else {
			record.RejectedReasons = append(record.RejectedReasons, "invalid_candidate_expansion_request")
		}
	}
	record.Accepted = len(record.Interpretations)+len(record.TieBreakingPreferences)+len(record.InvestigationPolicies)+len(record.Counterarguments)+len(expansions) > 0
	return record, expansions
}

func policiesFromInterpretations(values []domain.CognitiveInterpretation, verified []domain.VerifiedHypothesis) []domain.InvestigationPolicy {
	byID := map[string]domain.VerifiedHypothesis{}
	for _, item := range verified {
		byID[item.Draft.ID] = item
	}
	var policies []domain.InvestigationPolicy
	for _, value := range values {
		candidateIDs := append([]string(nil), value.CandidateIDs...)
		if len(candidateIDs) == 1 {
			if alternative, ok := nearestObjectiveCompetitor(candidateIDs[0], verified); ok {
				candidateIDs = append(candidateIDs, alternative)
			}
		}
		if len(candidateIDs) != 2 || candidateIDs[0] == candidateIDs[1] || byID[candidateIDs[0]].Draft.ID == "" || byID[candidateIDs[1]].Draft.ID == "" {
			continue
		}
		for _, observation := range value.RequiredObservations {
			if !allowedObservationKinds[observation] || len(sourcesForObservation(observation)) == 0 {
				continue
			}
			policies = append(policies, domain.InvestigationPolicy{
				CandidateIDs:        append([]string(nil), candidateIDs...),
				ObservationKind:     observation,
				RationalePredicates: append([]string(nil), value.ReasoningPredicates...),
			})
		}
	}
	return policies
}

func nearestObjectiveCompetitor(candidateID string, verified []domain.VerifiedHypothesis) (string, bool) {
	var selected *domain.VerifiedHypothesis
	for index := range verified {
		item := &verified[index]
		if item.Draft.ID == candidateID || item.Draft.ID == "" {
			continue
		}
		if selected == nil || abs(item.ObjectiveScore-verifiedScore(candidateID, verified)) < abs(selected.ObjectiveScore-verifiedScore(candidateID, verified)) ||
			(abs(item.ObjectiveScore-verifiedScore(candidateID, verified)) == abs(selected.ObjectiveScore-verifiedScore(candidateID, verified)) && item.Draft.ID < selected.Draft.ID) {
			selected = item
		}
	}
	if selected == nil {
		return "", false
	}
	return selected.Draft.ID, true
}

func verifiedScore(candidateID string, verified []domain.VerifiedHypothesis) float64 {
	for _, item := range verified {
		if item.Draft.ID == candidateID {
			return item.ObjectiveScore
		}
	}
	return 0
}

func uniqueInvestigationPolicies(values []domain.InvestigationPolicy) []domain.InvestigationPolicy {
	seen := map[string]bool{}
	out := make([]domain.InvestigationPolicy, 0, len(values))
	for _, value := range values {
		if len(value.CandidateIDs) != 2 {
			continue
		}
		key := value.CandidateIDs[0] + "\x00" + value.CandidateIDs[1] + "\x00" + value.ObservationKind
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, value)
	}
	return out
}

func validInterpretation(value domain.CognitiveInterpretation, assertions, candidates map[string]bool) bool {
	return len(value.SupportingAssertionIDs) > 0 && allKnown(value.SupportingAssertionIDs, assertions) && allKnown(value.CandidateIDs, candidates) && allPredicates(value.ReasoningPredicates) && allObservationKinds(value.RequiredObservations)
}

func validPreference(value domain.TieBreakingPreference, assertions, candidates map[string]bool, scores map[string]float64) bool {
	if !candidates[value.PreferredCandidateID] || !candidates[value.OtherCandidateID] || value.PreferredCandidateID == value.OtherCandidateID || !allKnown(value.AssertionIDs, assertions) || !allPredicates(value.Predicates) {
		return false
	}
	return abs(scores[value.PreferredCandidateID]-scores[value.OtherCandidateID]) <= .10
}

func allKnown(values []string, allowed map[string]bool) bool {
	if len(values) == 0 {
		return false
	}
	for _, value := range values {
		if !allowed[value] {
			return false
		}
	}
	return true
}

func allPredicates(values []string) bool {
	if len(values) == 0 {
		return false
	}
	for _, value := range values {
		if !allowedCognitivePredicates[value] {
			return false
		}
	}
	return true
}

func allObservationKinds(values []string) bool {
	if len(values) == 0 {
		return false
	}
	for _, value := range values {
		if !allowedObservationKinds[value] {
			return false
		}
	}
	return true
}

func filterKnownObservationKinds(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if allowedObservationKinds[value] {
			out = append(out, value)
		}
	}
	return out
}

func sortedCognitiveKeys(values map[string]bool) []string {
	out := make([]string, 0, len(values))
	for value := range values {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func abs(value float64) float64 {
	if value < 0 {
		return -value
	}
	return value
}
