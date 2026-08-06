package scorer

import (
	"strings"

	"github.com/kubepilot-aiops/kubepilot/benchmark/scenarios"
	"github.com/kubepilot-aiops/kubepilot/internal/domain"
)

type Score struct {
	RootCauseCorrect     bool    `json:"root_cause_correct"`
	StrictRootCause      bool    `json:"strict_root_cause"`
	SemanticRootCause    *bool   `json:"semantic_root_cause,omitempty"`
	SemanticConfidence   float64 `json:"semantic_confidence,omitempty"`
	SemanticReason       string  `json:"semantic_reason,omitempty"`
	CategoryCorrect      bool    `json:"category_correct"`
	VariantCorrect       bool    `json:"variant_correct"`
	ServiceCorrect       bool    `json:"service_correct"`
	ResourceCorrect      bool    `json:"resource_correct"`
	EvidencePrecision    float64 `json:"evidence_precision"`
	EvidenceRecall       float64 `json:"evidence_recall"`
	EvidenceGroundedness float64 `json:"evidence_groundedness"`
	DecisionCorrect      bool    `json:"decision_correct"`
}

func Incident(s scenarios.Scenario, in *domain.Incident) Score {
	out := Score{
		CategoryCorrect: equalNonEmpty(s.GroundTruth.RootCauseCategory, in.RootCauseCategory),
		VariantCorrect:  equalNonEmpty(s.Variant, in.RootCauseVariant),
		ServiceCorrect:  equalNonEmpty(s.GroundTruth.Service, in.RootCauseService),
		ResourceCorrect: equalNonEmpty(s.GroundTruth.Resource, in.RootCauseResource),
	}
	byID := map[string]domain.Evidence{}
	for _, e := range in.Evidence {
		byID[e.ID] = e
	}
	required := map[string]bool{}
	for _, kind := range s.GroundTruth.RequiredEvidence {
		required[strings.ToLower(kind)] = true
	}
	covered := map[string]bool{}
	relevantCitations, groundedCitations := 0, 0
	for _, id := range in.RootCauseEvidenceIDs {
		evidence, exists := byID[id]
		if !exists {
			continue
		}
		groundedCitations++
		matched := false
		for _, value := range []string{evidence.Kind, evidence.Source} {
			key := strings.ToLower(strings.TrimSpace(value))
			key = strings.TrimSuffix(strings.TrimSuffix(key, "_current"), "_trend")
			if required[key] {
				covered[key] = true
				matched = true
			}
		}
		if matched {
			relevantCitations++
		}
	}
	if len(in.RootCauseEvidenceIDs) > 0 {
		out.EvidencePrecision = float64(relevantCitations) / float64(len(in.RootCauseEvidenceIDs))
		out.EvidenceGroundedness = float64(groundedCitations) / float64(len(in.RootCauseEvidenceIDs))
	}
	if len(required) > 0 {
		out.EvidenceRecall = float64(len(covered)) / float64(len(required))
	}
	out.RootCauseCorrect = out.CategoryCorrect && out.VariantCorrect && out.ServiceCorrect && out.ResourceCorrect
	out.StrictRootCause = out.RootCauseCorrect && out.EvidenceRecall >= 0.5
	if in.Proposal != nil {
		for _, a := range s.GroundTruth.AllowedRecoveryActions {
			if equal(a, string(in.Proposal.Action)) && equal(s.Target, in.Proposal.Target) {
				out.DecisionCorrect = true
			}
		}
	}
	return out
}
func equal(a, b string) bool { return strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b)) }
func equalNonEmpty(a, b string) bool {
	return strings.TrimSpace(a) != "" && strings.TrimSpace(b) != "" && equal(a, b)
}

type CorrelationScore struct {
	ExactAccuracy float64 `json:"exact_accuracy"`
	Precision     float64 `json:"pairwise_precision"`
	Recall        float64 `json:"pairwise_recall"`
	F1            float64 `json:"pairwise_f1"`
	FalseMerges   int     `json:"false_merges"`
	FalseSplits   int     `json:"false_splits"`
}

func Correlation(expected, actual map[string]string) CorrelationScore {
	var tp, fp, fn int
	keys := make([]string, 0, len(expected))
	for k := range expected {
		keys = append(keys, k)
	}
	expectedGroups := map[string]map[string]bool{}
	actualGroups := map[string]map[string]bool{}
	for _, k := range keys {
		if expectedGroups[expected[k]] == nil {
			expectedGroups[expected[k]] = map[string]bool{}
		}
		expectedGroups[expected[k]][k] = true
		if actualGroups[actual[k]] == nil {
			actualGroups[actual[k]] = map[string]bool{}
		}
		actualGroups[actual[k]][k] = true
	}
	exact := 0
	for _, members := range expectedGroups {
		var actualID string
		for id := range members {
			actualID = actual[id]
			break
		}
		candidate := actualGroups[actualID]
		if len(candidate) != len(members) {
			continue
		}
		match := true
		for id := range members {
			if !candidate[id] {
				match = false
				break
			}
		}
		if match {
			exact++
		}
	}
	for i := 0; i < len(keys); i++ {
		for j := i + 1; j < len(keys); j++ {
			e := expected[keys[i]] == expected[keys[j]]
			a := actual[keys[i]] == actual[keys[j]]
			switch {
			case e && a:
				tp++
			case !e && a:
				fp++
			case e && !a:
				fn++
			}
		}
	}
	s := CorrelationScore{ExactAccuracy: float64(exact) / float64(max(1, len(expectedGroups))), FalseMerges: fp, FalseSplits: fn}
	s.Precision = float64(tp) / float64(max(1, tp+fp))
	s.Recall = float64(tp) / float64(max(1, tp+fn))
	if s.Precision+s.Recall > 0 {
		s.F1 = 2 * s.Precision * s.Recall / (s.Precision + s.Recall)
	}
	return s
}
