package agent

import (
	"encoding/json"
	"sort"

	"github.com/kubepilot-aiops/kubepilot/internal/domain"
)

func brainEvidenceViews(state *WorkflowState, items []domain.Evidence, maximumBytes, maximumItems int) []domain.BrainEvidenceView {
	if maximumItems <= 0 || maximumItems > len(items) {
		maximumItems = len(items)
	}
	hypothesesByEvidence := map[string][]string{}
	toolCallsByEvidence := map[string][]string{}
	artifactsByEvidence := map[string][]string{}
	if state != nil {
		for _, grounding := range state.HypothesisGroundings {
			ids := append(append([]string(nil), grounding.Evidence.SupportingEvidenceIDs...), grounding.Evidence.ContradictingEvidenceIDs...)
			for _, evidenceID := range ids {
				hypothesesByEvidence[evidenceID] = appendUnique(hypothesesByEvidence[evidenceID], grounding.HypothesisRevisionID)
			}
		}
		for _, execution := range state.ToolExecutions {
			provenance := execution.Result.Provenance
			for _, evidenceID := range provenance.EvidenceIDs {
				toolCallsByEvidence[evidenceID] = appendUnique(toolCallsByEvidence[evidenceID], provenance.ToolCallID)
				if provenance.RawArtifactHash != "" {
					artifactsByEvidence[evidenceID] = appendUnique(artifactsByEvidence[evidenceID], provenance.RawArtifactHash)
				}
			}
		}
	}

	views := make([]domain.BrainEvidenceView, 0, maximumItems)
	for _, item := range items {
		if len(views) >= maximumItems {
			break
		}
		observedAt := item.ObservedAt
		if observedAt.IsZero() {
			observedAt = item.Timestamp
		}
		kind := item.Type
		if kind == "" {
			kind = item.Kind
		}
		signals := append([]domain.EvidenceSignal(nil), item.Signals...)
		if len(signals) > 8 {
			signals = signals[:8]
		}
		view := domain.BrainEvidenceView{
			ID: item.ID, Source: string(item.Source), Kind: kind,
			Namespace: item.Namespace, Service: item.Service, Resource: item.Resource,
			WindowStart: item.WindowStart, WindowEnd: item.WindowEnd, ObservedAt: observedAt,
			Summary: truncateText(item.Summary, 512), Signals: signals,
			CausalNodeIDs:    append([]string(nil), item.CausalNodeIDs...),
			ContextRelevance: item.RelevanceScore, AnomalyScore: item.AnomalyScore, QualityScore: item.QualityScore,
			HypothesisRevisionIDs: append([]string(nil), hypothesesByEvidence[item.ID]...),
			ToolCallIDs:           append([]string(nil), toolCallsByEvidence[item.ID]...),
			RawArtifactHashes:     append([]string(nil), artifactsByEvidence[item.ID]...),
		}
		if len(view.CausalNodeIDs) > 8 {
			view.CausalNodeIDs = view.CausalNodeIDs[:8]
		}
		sort.Strings(view.HypothesisRevisionIDs)
		sort.Strings(view.ToolCallIDs)
		sort.Strings(view.RawArtifactHashes)
		trial := append(append([]domain.BrainEvidenceView(nil), views...), view)
		if maximumBytes > 0 {
			raw, _ := json.Marshal(trial)
			if len(raw) > maximumBytes {
				break
			}
		}
		views = append(views, view)
	}
	return views
}

func modelFacingBrainCapabilityOutput(state *WorkflowState, output brainCapabilityOutput) brainCapabilityOutput {
	projection := output
	projection.EvidenceView = brainEvidenceViews(state, output.Evidence, 32<<10, 12)
	projection.Evidence = nil
	projection.Candidates = compactToolCandidates(output.Candidates, 5)
	projection.Patterns = tailCausalPatterns(output.Patterns, 10)
	return projection
}
