package causal

import (
	"strings"

	"github.com/kubepilot-aiops/kubepilot/internal/domain"
)

type Pattern struct {
	ID               string   `json:"id" yaml:"id"`
	Category         string   `json:"category" yaml:"category"`
	Cause            string   `json:"cause" yaml:"cause"`
	Chain            []string `json:"chain" yaml:"chain"`
	RequiredEvidence []string `json:"required_evidence" yaml:"required_evidence"`
	Contradictions   []string `json:"contradictions" yaml:"contradictions"`
	Confidence       float64  `json:"confidence" yaml:"confidence"`
}

type PatternMatch struct {
	PatternID          string   `json:"pattern_id"`
	Category           string   `json:"category"`
	Cause              string   `json:"cause"`
	CausalPath         []string `json:"causal_path"`
	Coverage           float64  `json:"coverage"`
	MissingNodes       []string `json:"missing_nodes,omitempty"`
	RequiredEvidence   []string `json:"required_evidence,omitempty"`
	Contradictions     []string `json:"contradictions,omitempty"`
	ContradictionScore float64  `json:"contradiction_score"`
}

type HypothesisCausalEvidence struct {
	HypothesisID string   `json:"hypothesis_id"`
	CausalPath   []string `json:"causal_path"`
	Coverage     float64  `json:"coverage"`
	MissingNodes []string `json:"missing_nodes,omitempty"`
}

type ScoreInput struct {
	ModelPrior           float64
	EvidenceSupport      float64
	CausalCoverage       float64
	TopologyMatch        float64
	HistoricalSimilarity float64
	Contradiction        float64
}

type HypothesisScore struct {
	Score                float64            `json:"score"`
	EvidenceSupport      float64            `json:"evidence_support"`
	CausalCoverage       float64            `json:"causal_coverage"`
	TopologyMatch        float64            `json:"topology_match"`
	HistoricalSimilarity float64            `json:"historical_similarity"`
	ModelPrior           float64            `json:"model_prior"`
	Contradiction        float64            `json:"contradiction"`
	Breakdown            map[string]float64 `json:"breakdown"`
}

func PatternFromDomain(pattern domain.CausalPattern) Pattern {
	chain := make([]string, 0, len(pattern.Nodes))
	for _, node := range pattern.Nodes {
		chain = append(chain, node.ID)
	}
	if len(chain) == 0 {
		for _, edge := range pattern.Edges {
			chain = append(chain, edge.From, edge.To)
		}
	}
	return Pattern{ID: pattern.ID, Category: pattern.Category, Cause: pattern.Cause, Chain: unique(chain), Confidence: pattern.Confidence}
}

func unique(values []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, v := range values {
		v = strings.TrimSpace(strings.ToLower(v))
		if v != "" && !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}
