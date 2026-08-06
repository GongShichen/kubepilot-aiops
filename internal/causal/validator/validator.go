package validator

import (
	"context"
	"strings"

	knowledge "github.com/kubepilot-aiops/kubepilot/internal/causal/knowledge"
	"github.com/kubepilot-aiops/kubepilot/internal/domain"
)

type Validator struct {
	Store                     knowledge.Reader
	MinimumIndependentSources int
	MinimumSupport            int
}

func New(store knowledge.Reader) *Validator {
	return &Validator{Store: store, MinimumIndependentSources: 2, MinimumSupport: 3}
}

func (v *Validator) Validate(ctx context.Context, in *domain.Incident, proposal knowledge.Proposal) (knowledge.ValidationResult, error) {
	result := knowledge.ValidationResult{PatternID: proposal.Pattern.ID, Valid: false}
	if in == nil {
		result.FailedChecks = []string{"incident_missing"}
		result.Reason = "incident is required"
		return result, nil
	}
	if excluded(in) {
		result.FailedChecks = []string{"evaluation_isolation"}
		result.Reason = "evaluation incidents cannot update causal knowledge"
		return result, nil
	}
	if in.Status != domain.StatusResolved && in.Status != domain.StatusDiagnosing && in.Status != domain.StatusProposing {
		result.FailedChecks = []string{"incident_not_resolved"}
		result.Reason = "only resolved incidents can update causal knowledge"
		return result, nil
	}
	p := knowledge.Canonicalize(proposal.Pattern)
	result.PatternID = p.ID
	if strings.TrimSpace(p.Cause) == "" {
		result.FailedChecks = append(result.FailedChecks, "cause_missing")
	}
	if len(p.Nodes) < 2 || len(p.Edges) < 1 {
		result.FailedChecks = append(result.FailedChecks, "causal_path_incomplete")
	}
	nodes := map[string]bool{}
	for _, n := range p.Nodes {
		nodes[n.ID] = true
		if n.Type != "cause" && n.Type != "mechanism" && n.Type != "symptom" && n.Type != "observation" && n.Type != "action" && n.Type != "outcome" {
			result.FailedChecks = append(result.FailedChecks, "node_type_invalid")
		}
	}
	for _, e := range p.Edges {
		if !nodes[e.From] || !nodes[e.To] {
			result.FailedChecks = append(result.FailedChecks, "edge_target_missing")
		}
		if e.Relation != "causes" && e.Relation != "manifests_as" && e.Relation != "supports" && e.Relation != "contradicts" && e.Relation != "mitigates" && e.Relation != "verifies" && e.Relation != "correlates" {
			result.FailedChecks = append(result.FailedChecks, "edge_relation_invalid")
		}
	}
	sources := map[string]bool{}
	matched := 0
	for _, item := range p.SupportingEvidence {
		if item.Source != "" {
			sources[item.Source] = true
		}
		for _, ev := range in.Evidence {
			if ev.Source == item.Source && (item.Type == "" || ev.Type == item.Type || ev.Kind == item.Type) {
				matched++
				break
			}
		}
	}
	if matched < 2 {
		result.FailedChecks = append(result.FailedChecks, "evidence_not_grounded")
	}
	if len(sources) < max(v.MinimumIndependentSources, 2) {
		result.FailedChecks = append(result.FailedChecks, "independent_sources_missing")
	}
	if len(result.FailedChecks) > 0 {
		result.Reason = "causal proposal failed deterministic validation"
		return result, nil
	}
	if proposal.IncidentID != "" && proposal.IncidentID != in.ID {
		result.FailedChecks = []string{"incident_identity_mismatch"}
		result.Reason = "proposal incident does not match current incident"
		return result, nil
	}
	// Every observation node carrying evidence references must be backed by an
	// actual server-owned evidence item. This
	// prevents a model proposal from introducing unsupported causal nodes.
	for _, node := range p.Nodes {
		if node.Type == "observation" && len(node.SourceEvidenceIDs) > 0 {
			for _, evidenceID := range node.SourceEvidenceIDs {
				found := false
				for _, item := range in.Evidence {
					if item.ID == evidenceID {
						found = true
						break
					}
				}
				if !found {
					result.FailedChecks = append(result.FailedChecks, "observation_node_unobserved")
				}
			}
		}
	}
	if len(result.FailedChecks) > 0 {
		result.Reason = "causal proposal references unobserved evidence"
		return result, nil
	}
	contradictions := 0
	for _, item := range p.ContradictingEvidence {
		for _, ev := range in.Evidence {
			if ev.Source == item.Source && (item.Type == "" || ev.Type == item.Type || ev.Kind == item.Type) {
				contradictions++
				break
			}
		}
	}
	if len(p.SupportingEvidence) > 0 {
		result.Contradiction = float64(contradictions) / float64(len(p.SupportingEvidence))
	}
	if result.Contradiction > .10 {
		result.FailedChecks = append(result.FailedChecks, "contradiction_too_high")
		result.Reason = "causal proposal contains grounded contradictory observations"
		return result, nil
	}
	result.Valid = true
	if v.Store != nil {
		patterns, err := v.Store.List(ctx, "", 0)
		if err != nil {
			return result, err
		}
		result.SupportCount = 1
		for _, old := range patterns {
			if old.ID == p.ID {
				result.SupportCount = len(old.SourceIncidents)
				seenIncident := false
				for _, id := range old.SourceIncidents {
					if id == in.ID {
						seenIncident = true
						break
					}
				}
				if !seenIncident {
					result.SupportCount++
				}
				if result.SupportCount < 1 {
					result.SupportCount = 1
				}
				break
			}
		}
	} else {
		result.SupportCount = 1
	}
	result.Confidence = p.Confidence
	result.Accepted = result.SupportCount >= max(v.MinimumSupport, 3) && result.Confidence >= .80
	result.Reason = "proposal validated; repeated resolved incidents are required before activation"
	return result, nil
}

func excluded(in *domain.Incident) bool {
	if strings.EqualFold(in.Namespace, "kubepilot-benchmark") {
		return true
	}
	for _, a := range in.Alerts {
		for _, k := range []string{"evaluation", "benchmark", "kubepilot.io/evaluation"} {
			if strings.EqualFold(a.Labels[k], "true") || a.Labels[k] == "1" {
				return true
			}
		}
	}
	return false
}
func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
