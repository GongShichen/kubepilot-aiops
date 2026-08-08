package agent

import (
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/cloudwego/eino/schema"
	"github.com/kubepilot-aiops/kubepilot/internal/domain"
	"github.com/oklog/ulid/v2"
)

// This file belongs exclusively to the offline baseline implementations. None
// of these projections or the legacy RecoveryDecision can be reached by the
// production KubePilot Brain graph.

func mergeEvidenceToolMessages(s *WorkflowState, messages []*schema.Message) error {
	successful := map[string]bool{}
	seen := map[string]bool{}
	for _, item := range s.Incident.Evidence {
		seen[item.ID] = true
	}
	for _, message := range messages {
		var result evidenceToolResult
		if err := json.Unmarshal([]byte(message.Content), &result); err != nil {
			return err
		}
		if result.Error != "" {
			s.Errors = append(s.Errors, result.Source+": "+result.Error)
			continue
		}
		successful[result.Source] = true
		for _, item := range result.Evidence {
			item.Source = map[string]string{"metric": "prometheus", "log": "loki", "trace": "jaeger", "kubernetes": "kubernetes", "historical": "historical"}[result.Source]
			normalizeEvidence(&item, s.Incident)
			if !seen[item.ID] {
				seen[item.ID] = true
				s.Incident.Evidence = append(s.Incident.Evidence, item)
			}
		}
	}
	if !successful["kubernetes"] {
		return fmt.Errorf("kubernetes evidence unavailable")
	}
	sort.SliceStable(s.Incident.Evidence, func(i, j int) bool { return s.Incident.Evidence[i].Timestamp.Before(s.Incident.Evidence[j].Timestamp) })
	return nil
}

type evidenceToolResult struct {
	Source   string            `json:"source"`
	Evidence []domain.Evidence `json:"evidence,omitempty"`
	Error    string            `json:"error,omitempty"`
}

func baselineHypotheses(drafts []domain.HypothesisDraft) []domain.Hypothesis {
	out := make([]domain.Hypothesis, 0, len(drafts))
	for _, draft := range drafts {
		out = append(out, domain.Hypothesis{ID: draft.ID, Cause: draft.Cause, Probability: draft.PriorProbability, SupportingEvidence: append([]string(nil), draft.SupportingEvidenceIDs...), ContradictingEvidence: append([]string(nil), draft.ContradictingEvidenceIDs...), FalsificationConditions: append([]string(nil), draft.FalsificationConditions...)})
	}
	return out
}

type diagnosisEvidence struct {
	ID             string         `json:"id"`
	Source         string         `json:"source"`
	Type           string         `json:"type"`
	Timestamp      time.Time      `json:"timestamp,omitempty"`
	Namespace      string         `json:"namespace,omitempty"`
	Service        string         `json:"service,omitempty"`
	Resource       string         `json:"resource,omitempty"`
	Summary        string         `json:"summary"`
	Content        map[string]any `json:"content,omitempty"`
	RelevanceScore float64        `json:"relevance_score"`
	RankingReasons []string       `json:"ranking_reasons,omitempty"`
	CausalNodeIDs  []string       `json:"causal_node_ids,omitempty"`
}

func diagnosisEvidenceContext(items []domain.Evidence) []diagnosisEvidence {
	out := make([]diagnosisEvidence, 0, len(items))
	for _, item := range items {
		out = append(out, diagnosisEvidence{ID: item.ID, Source: item.Source, Type: item.Type, Timestamp: item.Timestamp, Namespace: item.Namespace, Service: item.Service, Resource: item.Resource, Summary: item.Summary, Content: item.Content, RelevanceScore: item.RelevanceScore, RankingReasons: append([]string(nil), item.RankingReasons...), CausalNodeIDs: append([]string(nil), item.CausalNodeIDs...)})
	}
	return out
}

type diagnosisPattern struct {
	ID         string              `json:"id"`
	Category   string              `json:"category"`
	Cause      string              `json:"cause"`
	NodeIDs    []string            `json:"node_ids"`
	Edges      []domain.CausalEdge `json:"edges"`
	Confidence float64             `json:"confidence"`
}

func diagnosisPatternContext(items []domain.CausalPattern) []diagnosisPattern {
	out := make([]diagnosisPattern, 0, len(items))
	for _, item := range items {
		nodes := make([]string, 0, len(item.Nodes))
		for _, node := range item.Nodes {
			nodes = append(nodes, node.ID)
		}
		out = append(out, diagnosisPattern{ID: item.ID, Category: item.Category, Cause: item.Cause, NodeIDs: nodes, Edges: append([]domain.CausalEdge(nil), item.Edges...), Confidence: item.Confidence})
	}
	return out
}

type diagnosisCandidate struct {
	IncidentID       string               `json:"incident_id"`
	Namespace        string               `json:"namespace"`
	Service          string               `json:"service"`
	Resource         string               `json:"resource"`
	Category         string               `json:"category"`
	RootCause        string               `json:"root_cause"`
	Summary          string               `json:"summary,omitempty"`
	Rank             domain.RankBreakdown `json:"rank"`
	RankingReasons   []string             `json:"ranking_reasons,omitempty"`
	TopologyServices []string             `json:"topology_services,omitempty"`
	CausalNodeIDs    []string             `json:"causal_node_ids,omitempty"`
}

func diagnosisCandidateContext(items []domain.RetrievalCandidate) []diagnosisCandidate {
	out := make([]diagnosisCandidate, 0, len(items))
	for _, item := range items {
		out = append(out, diagnosisCandidate{IncidentID: item.IncidentID, Namespace: item.Namespace, Service: item.Service, Resource: item.Resource, Category: item.Category, RootCause: item.RootCause, Summary: item.Summary, Rank: item.Rank, RankingReasons: append([]string(nil), item.RankingReasons...), TopologyServices: append([]string(nil), item.Features.TopologyServices...), CausalNodeIDs: append([]string(nil), item.Features.CausalNodeIDs...)})
	}
	return out
}

func recoveryProposal(in *domain.Incident, d RecoveryDecision) (*domain.RecoveryProposal, error) {
	target := in.RootCauseResource
	if target == "" {
		target = in.Resource
	}
	canonical, err := canonicalProposalTarget(d.Target, in.Namespace, target)
	if err != nil {
		return nil, err
	}
	return &domain.RecoveryProposal{ID: ulid.Make().String(), Action: d.Action, Namespace: in.Namespace, Target: canonical, Parameters: d.Parameters, Reason: d.Reason, Risk: d.Risk, Diff: d.Diff, Rollback: d.Rollback, Confidence: d.Confidence, ExpiresAt: time.Now().UTC().Add(15 * time.Minute)}, nil
}
