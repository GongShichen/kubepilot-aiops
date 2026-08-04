package discovery

import (
	"fmt"
	"sort"
	"strings"

	"github.com/kubepilot-aiops/kubepilot/internal/domain"
)

// Build is the concise package-level entry point used by server-side learning
// hooks and offline evaluators.
func Build(in *domain.Incident) (IncidentCausalGraph, error) {
	return BuildIncidentCausalGraph(in)
}

// BuildIncidentCausalGraph fuses the selected verified hypothesis, attributed
// Evidence, recovery result and observed topology. It accepts only resolved,
// successfully verified incidents; a proposal or a single unverified field is
// never sufficient to create discovery input.
func BuildIncidentCausalGraph(in *domain.Incident) (IncidentCausalGraph, error) {
	if in == nil {
		return IncidentCausalGraph{}, fmt.Errorf("incident is required")
	}
	if in.Status != domain.StatusResolved {
		return IncidentCausalGraph{}, fmt.Errorf("incident %s is not resolved", in.ID)
	}
	if excludedIncident(in) {
		return IncidentCausalGraph{}, fmt.Errorf("incident %s is excluded from knowledge discovery", in.ID)
	}
	if in.Verification == nil || !in.Verification.Success {
		return IncidentCausalGraph{}, fmt.Errorf("incident %s has no successful verification", in.ID)
	}
	selected := selectedHypothesis(in)
	if selected == nil {
		return IncidentCausalGraph{}, fmt.Errorf("incident %s has no selected verified hypothesis", in.ID)
	}
	if selected.ContradictionScore > .10 {
		return IncidentCausalGraph{}, fmt.Errorf("incident %s selected hypothesis has excessive contradiction", in.ID)
	}
	selectedConfidence := latestConfidence(*selected)
	if selectedConfidence <= 0 {
		selectedConfidence = selected.FinalScore
	}
	if selectedConfidence < .80 {
		return IncidentCausalGraph{}, fmt.Errorf("incident %s selected hypothesis is below discovery confidence threshold", in.ID)
	}
	path := append([]string(nil), selected.Draft.ExpectedCausalPath...)
	if len(path) == 0 {
		return IncidentCausalGraph{}, fmt.Errorf("incident %s has no causal path", in.ID)
	}
	if cause := strings.TrimSpace(selected.Draft.Cause); cause != "" && !sameName(cause, path[0]) {
		path = append([]string{cause}, path...)
	}
	path = normalizePath(path)
	if len(path) < 2 {
		return IncidentCausalGraph{}, fmt.Errorf("incident %s causal path is too short", in.ID)
	}

	out := IncidentCausalGraph{IncidentID: in.ID}
	pathIDs := make([]string, 0, len(path))
	pathConfidence := latestConfidence(*selected)
	if pathConfidence <= 0 {
		pathConfidence = selected.FinalScore
	}
	if pathConfidence <= 0 {
		pathConfidence = in.Confidence
	}
	pathConfidence = clamp(pathConfidence)
	for i, name := range path {
		typ := NodeSymptom
		if i == 0 {
			typ = NodeCause
		}
		id := fmt.Sprintf("path:%d:%s", i, name)
		pathIDs = append(pathIDs, id)
		out.Nodes = append(out.Nodes, CausalNode{ID: id, Type: typ, Name: name, Confidence: pathConfidence})
		if i > 0 {
			out.Edges = append(out.Edges, CausalEdge{Source: pathIDs[i-1], Target: id, Relation: "causes", Confidence: pathConfidence})
		}
	}

	selectedEvidence := unique(append(append([]string(nil), selected.Draft.SupportingEvidenceIDs...), in.RootCauseEvidenceIDs...))
	for _, evidence := range in.Evidence {
		if len(selectedEvidence) > 0 && !contains(selectedEvidence, evidence.ID) {
			continue
		}
		id := evidenceNodeID(evidence)
		typ := evidenceNodeType(evidence)
		quality := evidence.Confidence
		if evidence.Attribution != nil && evidence.Attribution.AttributionScore > quality {
			quality = evidence.Attribution.AttributionScore
		}
		if quality <= 0 {
			quality = .5
		}
		out.Nodes = append(out.Nodes, CausalNode{ID: id, Type: typ, Name: evidenceName(evidence), Confidence: clamp(quality), SourceEvidenceIDs: []string{evidence.ID}})
		target := pathIDs[0]
		if len(pathIDs) > 1 && evidence.Type != "" && (evidence.Type == "symptom" || evidence.Type == "business") {
			target = pathIDs[len(pathIDs)-1]
		}
		out.Edges = append(out.Edges, CausalEdge{Source: id, Target: target, Relation: "supports", Confidence: clamp(quality)})
	}

	if in.Proposal != nil && strings.TrimSpace(string(in.Proposal.Action)) != "" {
		id := "action:" + strings.ToLower(strings.TrimSpace(string(in.Proposal.Action)))
		out.Nodes = append(out.Nodes, CausalNode{ID: id, Type: NodeAction, Name: strings.ToLower(strings.TrimSpace(string(in.Proposal.Action))), Confidence: clamp(in.Proposal.Confidence)})
		out.Edges = append(out.Edges, CausalEdge{Source: pathIDs[len(pathIDs)-1], Target: id, Relation: "precedes", Confidence: clamp(in.Proposal.Confidence)})
	}
	resultID := "recovery:" + boolName(in.Verification.Success)
	out.Nodes = append(out.Nodes, CausalNode{ID: resultID, Type: NodeRecoveryResult, Name: boolName(in.Verification.Success), Confidence: 1})
	resultSource := pathIDs[len(pathIDs)-1]
	if in.Proposal != nil && strings.TrimSpace(string(in.Proposal.Action)) != "" {
		resultSource = "action:" + strings.ToLower(strings.TrimSpace(string(in.Proposal.Action)))
	}
	out.Edges = append(out.Edges, CausalEdge{Source: resultSource, Target: resultID, Relation: "supports", Confidence: 1})
	// Topology is supporting context rather than a causal assertion. Preserve
	// it as correlates edges so the miner cannot mistake a dependency edge for
	// a discovered cause.
	if in.DiagnosisLedger != nil && len(in.DiagnosisLedger.Candidates) > 0 {
		observed := in.DiagnosisLedger.Candidates[0].Features.TopologyGraph
		for _, node := range observed.Nodes {
			name := normalizeName(node.ID)
			if name == "" {
				continue
			}
			id := "topology:" + name
			out.Nodes = append(out.Nodes, CausalNode{ID: id, Type: NodeSymptom, Name: name, Confidence: .5})
		}
		for _, edge := range observed.Edges {
			from := "topology:" + normalizeName(edge.From)
			to := "topology:" + normalizeName(edge.To)
			out.Edges = append(out.Edges, CausalEdge{Source: from, Target: to, Relation: "correlates", Confidence: .5})
		}
	}
	if len(outEvidence(out)) == 0 {
		return IncidentCausalGraph{}, fmt.Errorf("incident %s causal graph has no grounded Evidence", in.ID)
	}
	return normalizeGraph(out), nil
}

func selectedHypothesis(in *domain.Incident) *domain.VerifiedHypothesis {
	if in.DiagnosisLedger == nil {
		return nil
	}
	for i := range in.DiagnosisLedger.Verified {
		candidate := &in.DiagnosisLedger.Verified[i]
		if candidate.Draft.ID == in.DiagnosisLedger.SelectedHypothesisID && candidate.Status != domain.HypothesisRefuted {
			return candidate
		}
	}
	return nil
}

func latestConfidence(h domain.VerifiedHypothesis) float64 {
	if len(h.ConfidenceHistory) == 0 {
		return h.FinalScore
	}
	return h.ConfidenceHistory[len(h.ConfidenceHistory)-1].Score
}

func evidenceNodeType(e domain.Evidence) NodeType {
	typ := strings.ToLower(strings.TrimSpace(e.Type))
	if typ == "" {
		typ = strings.ToLower(strings.TrimSpace(e.Kind))
	}
	switch {
	case e.Source == "prometheus", typ == "metric", typ == "metrics":
		return NodeMetric
	case e.Source == "loki", typ == "log", typ == "log_pattern", typ == "error_log":
		return NodeLogPattern
	case e.Source == "jaeger", typ == "trace", typ == "trace_pattern", typ == "span":
		return NodeTracePattern
	case e.Source == "kubernetes", typ == "kubernetes_event", typ == "event":
		return NodeKubernetesEvent
	default:
		return NodeSymptom
	}
}

func evidenceNodeID(e domain.Evidence) string {
	if e.ID != "" {
		return "evidence:" + e.ID
	}
	return "evidence:" + strings.ToLower(strings.TrimSpace(e.Source)) + ":" + evidenceName(e)
}

func evidenceName(e domain.Evidence) string {
	if value := strings.TrimSpace(e.Summary); value != "" {
		return normalizeName(value)
	}
	return normalizeName(string(e.Source) + " " + string(e.Type))
}

func normalizePath(path []string) []string {
	out := make([]string, 0, len(path))
	for _, item := range path {
		name := normalizeName(item)
		if name == "" || (len(out) > 0 && out[len(out)-1] == name) {
			continue
		}
		out = append(out, name)
	}
	return out
}

func normalizeGraph(graph IncidentCausalGraph) IncidentCausalGraph {
	sort.SliceStable(graph.Nodes, func(i, j int) bool { return graph.Nodes[i].ID < graph.Nodes[j].ID })
	sort.SliceStable(graph.Edges, func(i, j int) bool {
		left := graph.Edges[i].Source + ":" + graph.Edges[i].Target + ":" + graph.Edges[i].Relation
		right := graph.Edges[j].Source + ":" + graph.Edges[j].Target + ":" + graph.Edges[j].Relation
		return left < right
	})
	return graph
}

func outEvidence(graph IncidentCausalGraph) []CausalNode {
	out := []CausalNode{}
	for _, node := range graph.Nodes {
		switch node.Type {
		case NodeMetric, NodeLogPattern, NodeTracePattern, NodeKubernetesEvent:
			out = append(out, node)
		}
	}
	return out
}

func excludedIncident(in *domain.Incident) bool {
	if strings.EqualFold(strings.TrimSpace(in.Namespace), "kubepilot-benchmark") {
		return true
	}
	for _, alert := range in.Alerts {
		for _, key := range []string{"evaluation", "benchmark", "kubepilot.io/evaluation"} {
			if strings.EqualFold(alert.Labels[key], "true") || alert.Labels[key] == "1" {
				return true
			}
		}
	}
	return false
}

func sameName(left, right string) bool { return normalizeName(left) == normalizeName(right) }
func contains(values []string, value string) bool {
	for _, item := range values {
		if item == value {
			return true
		}
	}
	return false
}
func boolName(value bool) string {
	if value {
		return "verification_success"
	}
	return "verification_failed"
}
func clamp(value float64) float64 {
	if value < 0 {
		return 0
	}
	if value > 1 {
		return 1
	}
	return value
}
