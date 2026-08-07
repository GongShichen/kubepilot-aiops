package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	openmodel "github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/kubepilot-aiops/kubepilot/internal/domain"
)

func planRequests(plan domain.InvestigationPlan, incident *domain.Incident) []domain.EvidenceRequest {
	out := make([]domain.EvidenceRequest, 0, len(plan.Tasks))
	for _, task := range plan.Tasks {
		request := task.Request
		if request.Source == "" {
			request = defaultEvidenceRequest(incident, task.Source)
		}
		request.HypothesisIDs = append([]string(nil), task.HypothesisIDs...)
		out = append(out, request)
	}
	return out
}

func (r *AgentRegistry) generateRole(ctx context.Context, agentName, instruction, payload string) (*schema.Message, error) {
	skill, ok := r.skills[agentName]
	if !ok {
		return nil, fmt.Errorf("skill for %s is not registered", agentName)
	}
	system := skill.Content + "\n\nRuntime instruction: " + instruction + "\nKeep the complete generated response concise and finish the required JSON well within the configured output-token limit."
	messages := []*schema.Message{schema.SystemMessage(system), schema.UserMessage(payload)}
	response, err := r.chat.Generate(ctx, messages, r.structuredModelOptions()...)
	if err != nil || validStructuredResponse(response) {
		return response, err
	}
	// Some reasoning-capable providers can finish a streamed response with only
	// hidden reasoning content. Retry that protocol failure once as a fresh,
	// explicitly-visible JSON response. The retry preserves the configured
	// per-response output limit and aggregates both attempts into usage telemetry.
	retrySystem := system + "\n\nRetry requirement: the prior response did not contain a valid visible JSON object. Return the requested JSON object immediately, with no prose or hidden analysis."
	retryMessages := []*schema.Message{schema.SystemMessage(retrySystem), schema.UserMessage(payload)}
	retry, retryErr := r.chat.Generate(ctx, retryMessages, r.structuredModelOptions()...)
	if retryErr != nil {
		return response, fmt.Errorf("retry visible JSON response: %w", retryErr)
	}
	mergeResponseUsage(retry, response)
	return retry, nil
}

func validStructuredResponse(message *schema.Message) bool {
	if message == nil {
		return false
	}
	object, err := modelJSONObject(message.Content)
	return err == nil && json.Valid([]byte(object))
}

func mergeResponseUsage(final, previous *schema.Message) {
	if final == nil || previous == nil || previous.ResponseMeta == nil || previous.ResponseMeta.Usage == nil {
		return
	}
	if final.ResponseMeta == nil {
		final.ResponseMeta = &schema.ResponseMeta{}
	}
	if final.ResponseMeta.Usage == nil {
		usage := *previous.ResponseMeta.Usage
		final.ResponseMeta.Usage = &usage
		return
	}
	current := final.ResponseMeta.Usage
	prior := previous.ResponseMeta.Usage
	current.PromptTokens += prior.PromptTokens
	current.PromptTokenDetails.CachedTokens += prior.PromptTokenDetails.CachedTokens
	current.CompletionTokens += prior.CompletionTokens
	current.CompletionTokensDetails.ReasoningTokens += prior.CompletionTokensDetails.ReasoningTokens
	current.TotalTokens += prior.TotalTokens
}

func (r *AgentRegistry) structuredModelOptions() []model.Option {
	options := append([]model.Option(nil), r.modelOptions()...)
	// Baseline cognitive roles return server-validated protocol objects rather than
	// free-form prose. Request JSON mode only for these calls: ReAct continues
	// to use normal tool calling, and hidden reasoning is never parsed or
	// retained as a structured result.
	return append(options, openmodel.WithExtraFields(map[string]any{
		"response_format": map[string]string{"type": "json_object"},
	}))
}

func structuredOutputError(agentName string, message *schema.Message, err error) error {
	if message == nil {
		return fmt.Errorf("%s structured output: %w", agentName, err)
	}
	// Report lengths only. Reasoning content is neither parsed nor persisted as
	// structured output, keeping hidden reasoning outside the audit record.
	return fmt.Errorf("%s structured output (final_bytes=%d reasoning_bytes=%d): %w", agentName, len(message.Content), len(message.ReasoningContent), err)
}

func arbitrateHypotheses(verified []domain.VerifiedHypothesis, evidence []domain.Evidence) domain.ArbitrationResult {
	ordered := append([]domain.VerifiedHypothesis(nil), verified...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].FinalScore == ordered[j].FinalScore {
			return ordered[i].Draft.ID < ordered[j].Draft.ID
		}
		return ordered[i].FinalScore > ordered[j].FinalScore
	})
	result := domain.ArbitrationResult{NeedsMoreEvidence: true, Reason: "no hypothesis satisfied deterministic acceptance gates"}
	for _, item := range ordered {
		result.RankedHypothesisIDs = append(result.RankedHypothesisIDs, item.Draft.ID)
	}
	if len(ordered) == 0 {
		// An empty deterministic candidate universe is an auditable abstention,
		// not an absent arbitration record.  Downstream monitoring uses gate
		// results to distinguish evidence collection failures from a safe
		// "unresolved mechanism" outcome and to apply the consecutive-gate pause
		// rule consistently.
		arbitrationGateFailure.WithLabelValues("no_candidate").Inc()
		result.GateResults = []domain.HypothesisGateResult{{FailedGates: []string{"no_candidate"}}}
		return result
	}
	result.SelectedHypothesisID = ordered[0].Draft.ID
	result.SelectedScore = ordered[0].FinalScore
	if len(ordered) == 1 {
		result.ScoreMargin = ordered[0].FinalScore
	} else {
		result.ScoreMargin = ordered[0].FinalScore - ordered[1].FinalScore
	}
	for index, item := range ordered {
		failed := hypothesisGateFailures(item, evidence)
		if index == 0 && result.ScoreMargin < .15 {
			failed = append(failed, "score_margin")
		}
		for _, gate := range failed {
			arbitrationGateFailure.WithLabelValues(gate).Inc()
		}
		breakdown := domain.HypothesisConfidenceRecord{
			HypothesisID: item.Draft.ID, Score: item.FinalScore, ObjectiveScore: item.ObjectiveScore, ObservationCoverage: item.ObservationCoverage,
			ModelPrior:      item.Draft.PriorProbability,
			SupportingScore: item.SupportingScore, ContradictionScore: item.ContradictionScore,
			CausalPathCoverage: item.CausalPathCoverage, HistoricalRelevance: item.HistoricalRelevance,
			TopologyRelevance: item.TopologyRelevance, ComputedAt: time.Now().UTC(),
			EvidenceSourceCount: evidenceSourceCount(item.VerifiedEvidenceIDs, evidence),
		}
		if len(item.ConfidenceHistory) > 0 {
			breakdown = item.ConfidenceHistory[len(item.ConfidenceHistory)-1]
		}
		result.GateResults = append(result.GateResults, domain.HypothesisGateResult{HypothesisID: item.Draft.ID, ScoreBreakdown: breakdown, FailedGates: failed})
	}
	ranked := rankRootCause(rootRankInput{Verified: ordered, Evidence: evidence})
	result.Accepted = ranked.Selected != nil && ranked.Selected.Draft.ID == ordered[0].Draft.ID && result.ScoreMargin >= .15
	result.NeedsMoreEvidence = !result.Accepted
	if result.Accepted {
		result.Reason = "highest-ranked hypothesis passed evidence, contradiction, confidence, and margin gates"
	}
	return result
}

func hypothesisGateFailures(item domain.VerifiedHypothesis, evidence []domain.Evidence) []string {
	var failed []string
	if item.Status != domain.HypothesisSupported && item.Status != domain.HypothesisAccepted {
		failed = append(failed, "supported_status")
	}
	if item.SupportingScore < .65 {
		failed = append(failed, "supporting_score")
	}
	if len(item.MissingCausalNodes) > 0 || item.CausalPathCoverage < 1 {
		failed = append(failed, "causal_coverage")
	}
	if item.FinalScore < .80 {
		failed = append(failed, "final_score")
	}
	if item.ContradictionScore > .10 {
		failed = append(failed, "contradiction")
	}
	if len(item.VerifiedEvidenceIDs) < 2 {
		failed = append(failed, "evidence_count")
	}
	sources, hasKubernetes := evidenceSources(item.VerifiedEvidenceIDs, evidence)
	if len(sources) < 2 {
		failed = append(failed, "independent_sources")
	}
	if !hasKubernetes {
		failed = append(failed, "kubernetes_evidence")
	}
	return failed
}

func evidenceSources(ids []string, evidence []domain.Evidence) (map[string]bool, bool) {
	allowed := map[string]domain.Evidence{}
	for _, item := range evidence {
		allowed[item.ID] = item
	}
	sources := map[string]bool{}
	hasKubernetes := false
	for _, id := range ids {
		if item, ok := allowed[id]; ok {
			sources[item.Source] = true
			hasKubernetes = hasKubernetes || item.Source == "kubernetes"
		}
	}
	return sources, hasKubernetes
}

func evidenceSourceCount(ids []string, evidence []domain.Evidence) int {
	sources, _ := evidenceSources(ids, evidence)
	return len(sources)
}

func allowedEvidenceTargets(incident *domain.Incident, evidence []domain.Evidence) map[string]bool {
	allowed := map[string]bool{resourceIdentity(incident.Service, incident.Resource): true}
	for _, item := range evidence {
		facts := item.Facts
		if len(facts) == 0 {
			facts = item.Content
		}
		for _, key := range []string{"discovered_dependencies", "dependencies"} {
			for _, dependency := range stringSlice(facts[key]) {
				allowed[resourceIdentity(dependency, dependency)] = true
			}
		}
		for _, key := range []string{"slow_service", "error_service", "dependency"} {
			if value, ok := facts[key].(string); ok && value != "" {
				allowed[resourceIdentity(value, value)] = true
			}
		}
	}
	return allowed
}

func stringSlice(value any) []string {
	switch typed := value.(type) {
	case []string:
		return typed
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if value, ok := item.(string); ok {
				out = append(out, value)
			}
		}
		return out
	default:
		return nil
	}
}

func filterGroundedHypothesisDrafts(drafts []domain.HypothesisDraft, evidence []domain.Evidence, patternSets ...[]domain.CausalPattern) []domain.HypothesisDraft {
	allowed := make(map[string]struct{}, len(evidence))
	patterns := flattenCausalPatterns(patternSets...)
	allowedNodes := causalNodeAllowlist(evidence, patterns)
	allowedEdges := causalEdgeAllowlist(evidence, patterns)
	for _, item := range evidence {
		if item.ID != "" {
			allowed[item.ID] = struct{}{}
		}
	}
	out := make([]domain.HypothesisDraft, 0, len(drafts))
	for _, draft := range drafts {
		if draft.ID == "" || len(draft.SupportingEvidenceIDs) == 0 || len(draft.ExpectedCausalNodeIDs) == 0 {
			continue
		}
		if !allEvidenceReferencesKnown(draft.SupportingEvidenceIDs, allowed) || !allEvidenceReferencesKnown(draft.ContradictingEvidenceIDs, allowed) {
			continue
		}
		if !allEvidenceReferencesKnown(draft.ExpectedCausalNodeIDs, allowedNodes) || !causalPathIsServerValid(draft.ExpectedCausalNodeIDs, draft.SupportingEvidenceIDs, allowedEdges) {
			continue
		}
		// The baseline causal-learning schema stores canonical server node IDs in
		// ExpectedCausalPath.
		draft.ExpectedCausalPath = append([]string(nil), draft.ExpectedCausalNodeIDs...)
		out = append(out, draft)
	}
	return out
}

func allowedCausalNodes(evidence []domain.Evidence, patterns []domain.CausalPattern) []map[string]string {
	values := causalNodeAllowlist(evidence, patterns)
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]map[string]string, 0, len(keys))
	for _, key := range keys {
		value := map[string]string{"id": key, "type": "observed_pattern_node"}
		if strings.HasPrefix(key, "obs:") {
			value["type"] = "observation"
			value["evidence_id"] = strings.TrimPrefix(key, "obs:")
		}
		out = append(out, value)
	}
	return out
}

func allowedCausalEdges(evidence []domain.Evidence, patterns []domain.CausalPattern) []map[string]string {
	edges := causalEdgeAllowlist(evidence, patterns)
	keys := make([]string, 0, len(edges))
	for key := range edges {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]map[string]string, 0, len(keys))
	for _, key := range keys {
		parts := strings.SplitN(key, "\x00", 2)
		out = append(out, map[string]string{"from": parts[0], "to": parts[1]})
	}
	return out
}

func flattenCausalPatterns(groups ...[]domain.CausalPattern) []domain.CausalPattern {
	var out []domain.CausalPattern
	for _, group := range groups {
		out = append(out, group...)
	}
	return out
}

func causalNodeAllowlist(evidence []domain.Evidence, patterns []domain.CausalPattern) map[string]struct{} {
	patternNodes := map[string]struct{}{}
	for _, pattern := range patterns {
		if pattern.Status != "active" {
			continue
		}
		for _, node := range pattern.Nodes {
			patternNodes[node.ID] = struct{}{}
		}
	}
	allowed := map[string]struct{}{}
	for _, item := range evidence {
		if item.ID == "" {
			continue
		}
		allowed["obs:"+item.ID] = struct{}{}
		for _, nodeID := range item.CausalNodeIDs {
			if _, isPatternNode := patternNodes[nodeID]; isPatternNode {
				allowed[nodeID] = struct{}{}
			}
		}
	}
	return allowed
}

func causalEdgeAllowlist(evidence []domain.Evidence, patterns []domain.CausalPattern) map[string]struct{} {
	nodes := causalNodeAllowlist(evidence, patterns)
	edges := map[string]struct{}{}
	for _, pattern := range patterns {
		if pattern.Status != "active" {
			continue
		}
		for _, edge := range pattern.Edges {
			if _, fromObserved := nodes[edge.From]; !fromObserved {
				continue
			}
			if _, toObserved := nodes[edge.To]; !toObserved {
				continue
			}
			edges[edge.From+"\x00"+edge.To] = struct{}{}
		}
	}
	return edges
}

func causalPathIsServerValid(expected, supporting []string, edges map[string]struct{}) bool {
	if len(expected) == 0 {
		return false
	}
	if len(expected) == 1 {
		if !strings.HasPrefix(expected[0], "obs:") {
			return true
		}
		return slices.Contains(supporting, strings.TrimPrefix(expected[0], "obs:"))
	}
	for _, nodeID := range expected {
		if strings.HasPrefix(nodeID, "obs:") {
			return false
		}
	}
	for index := 0; index < len(expected)-1; index++ {
		if _, ok := edges[expected[index]+"\x00"+expected[index+1]]; !ok {
			return false
		}
	}
	return true
}

func allEvidenceReferencesKnown(ids []string, allowed map[string]struct{}) bool {
	for _, id := range ids {
		if _, ok := allowed[id]; !ok {
			return false
		}
	}
	return true
}
