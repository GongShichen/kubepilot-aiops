package agent

import (
	"encoding/json"
	"sort"
	"time"

	"github.com/kubepilot-aiops/kubepilot/internal/domain"
)

type brainHybridRetrievalView struct {
	Channels     map[domain.RetrievalChannel]int    `json:"channel_candidate_counts"`
	Unavailable  []domain.RetrievalChannel          `json:"unavailable_channels,omitempty"`
	Errors       map[domain.RetrievalChannel]string `json:"channel_errors,omitempty"`
	Final        []domain.RetrievalCandidate        `json:"final"`
	RerankerUsed bool                               `json:"reranker_used"`
	SnapshotHash string                             `json:"snapshot_hash"`
	RetrievedAt  time.Time                          `json:"retrieved_at"`
}

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

func brainWorldModelView(model *domain.OperationalWorldModel) *domain.OperationalWorldModel {
	if model == nil {
		return nil
	}
	view := *model
	view.Entities = boundedSlice(model.Entities, 20)
	view.Relations = boundedSlice(model.Relations, 30)
	view.AbnormalSignals = boundedSlice(model.AbnormalSignals, 20)
	view.Timeline = boundedSlice(model.Timeline, 12)
	view.MetricSignatures = boundedSlice(model.MetricSignatures, 12)
	return &view
}

func brainHybridRetrievalViews(values []domain.HybridRetrievalResult, limit int) []brainHybridRetrievalView {
	if limit <= 0 || len(values) == 0 {
		return nil
	}
	start := len(values) - limit
	if start < 0 {
		start = 0
	}
	views := make([]brainHybridRetrievalView, 0, len(values)-start)
	for _, value := range values[start:] {
		view := brainHybridRetrievalView{Channels: map[domain.RetrievalChannel]int{}, Errors: map[domain.RetrievalChannel]string{}, Final: compactToolCandidates(value.Final, 5), RerankerUsed: value.RerankerUsed, SnapshotHash: value.SnapshotHash, RetrievedAt: value.RetrievedAt}
		for _, channel := range value.Channels {
			view.Channels[channel.Channel] = len(channel.Candidates)
			if !channel.Available {
				view.Unavailable = append(view.Unavailable, channel.Channel)
			}
			if channel.Error != "" {
				view.Errors[channel.Channel] = channel.Error
			}
		}
		if len(view.Errors) == 0 {
			view.Errors = nil
		}
		views = append(views, view)
	}
	return views
}

func boundedSlice[T any](values []T, limit int) []T {
	if limit <= 0 || len(values) == 0 {
		return nil
	}
	if len(values) <= limit {
		return append([]T(nil), values...)
	}
	return append([]T(nil), values[len(values)-limit:]...)
}

func modelFacingBrainCapabilityOutput(state *WorkflowState, output brainCapabilityOutput) brainCapabilityOutput {
	projection := output
	projection.EvidenceView = brainEvidenceViews(state, output.Evidence, 32<<10, 12)
	projection.Evidence = nil
	projection.HistoricalIncidents = compactToolCandidates(output.HistoricalIncidents, 5)
	if projection.HybridRetrieval != nil {
		copyResult := *projection.HybridRetrieval
		copyResult.Fused = compactToolCandidates(copyResult.Fused, 10)
		copyResult.Final = compactToolCandidates(copyResult.Final, 5)
		for index := range copyResult.Channels {
			copyResult.Channels[index].Candidates = compactToolCandidates(copyResult.Channels[index].Candidates, 5)
		}
		projection.HybridRetrieval = &copyResult
	}
	projection.Patterns = tailCausalPatterns(output.Patterns, 10)
	return projection
}
