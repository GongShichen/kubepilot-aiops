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
	return &Validator{Store: store, MinimumIndependentSources: 2, MinimumSupport: 2}
}

func (v *Validator) Validate(ctx context.Context, in *domain.Incident, proposal knowledge.Proposal) (knowledge.ValidationResult, error) {
	result := knowledge.ValidationResult{PatternID: proposal.Pattern.PatternID, Valid: false}
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
	result.PatternID = p.PatternID
	if strings.TrimSpace(p.Cause) == "" {
		result.FailedChecks = append(result.FailedChecks, "cause_missing")
	}
	if len(p.CausalGraph.Nodes) < 2 || len(p.CausalGraph.Edges) < 1 {
		result.FailedChecks = append(result.FailedChecks, "causal_path_incomplete")
	}
	nodes := map[string]bool{}
	for _, n := range p.CausalGraph.Nodes {
		nodes[n.ID] = true
		if n.Type != "cause" && n.Type != "symptom" && n.Type != "evidence" && n.Type != "action" {
			result.FailedChecks = append(result.FailedChecks, "node_type_invalid")
		}
	}
	for _, e := range p.CausalGraph.Edges {
		if !nodes[e.Source] || !nodes[e.Target] {
			result.FailedChecks = append(result.FailedChecks, "edge_target_missing")
		}
		if e.Relation != "causes" && e.Relation != "supports" && e.Relation != "contradicts" {
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
	// Every evidence node must be backed by an actual evidence item. This
	// prevents a model proposal from introducing unsupported causal nodes.
	for _, node := range p.CausalGraph.Nodes {
		if node.Type == "evidence" {
			found := false
			for _, item := range in.Evidence {
				if strings.EqualFold(item.Type, node.Name) || strings.EqualFold(item.Kind, node.Name) || strings.EqualFold(item.Source, node.Name) {
					found = true
					break
				}
			}
			if !found {
				result.FailedChecks = append(result.FailedChecks, "evidence_node_unobserved")
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
			if old.PatternID == p.PatternID {
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
	result.Confidence = .25*p.Confidence + .75*(1-approxExp(-float64(result.SupportCount)/4))
	if result.Confidence > 1 {
		result.Confidence = 1
	}
	result.Accepted = result.SupportCount >= max(v.MinimumSupport, 2) && result.Confidence >= .80
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
func approxExp(v float64) float64 {
	term, sum := 1.0, 1.0
	for i := 1; i < 18; i++ {
		term *= v / float64(i)
		sum += term
	}
	return sum
}
func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
