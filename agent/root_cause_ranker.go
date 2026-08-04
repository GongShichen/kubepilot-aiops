package agent

import (
	"sort"

	"github.com/kubepilot-aiops/kubepilot/internal/domain"
)

type rootRankInput struct {
	Verified []domain.VerifiedHypothesis
	Evidence []domain.Evidence
}

type rootRankOutput struct {
	Selected                  *domain.VerifiedHypothesis
	NeedsAttention            bool
	RequestAdditionalEvidence bool
}

// rankRootCause is deterministic and evidence-gated. The Diagnosis Agent may
// explore and submit drafts, but it cannot select a root cause outside this
// ranker. Missing causal nodes request one bounded supplementary collection;
// all other gate failures require human attention.
func rankRootCause(input rootRankInput) rootRankOutput {
	ordered := append([]domain.VerifiedHypothesis(nil), input.Verified...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].FinalScore == ordered[j].FinalScore {
			return ordered[i].Draft.ID < ordered[j].Draft.ID
		}
		return ordered[i].FinalScore > ordered[j].FinalScore
	})
	for index := range ordered {
		candidate := &ordered[index]
		if candidate.Status != domain.HypothesisSupported && candidate.Status != domain.HypothesisAccepted {
			continue
		}
		if len(candidate.MissingCausalNodes) > 0 {
			return rootRankOutput{RequestAdditionalEvidence: true}
		}
		sources := map[string]bool{}
		hasKubernetes := false
		allowed := map[string]domain.Evidence{}
		for _, evidence := range input.Evidence {
			allowed[evidence.ID] = evidence
		}
		for _, id := range candidate.VerifiedEvidenceIDs {
			if evidence, ok := allowed[id]; ok {
				sources[evidence.Source] = true
				if evidence.Source == "kubernetes" {
					hasKubernetes = true
				}
			}
		}
		if candidate.FinalScore >= .80 && candidate.ContradictionScore <= .10 && len(candidate.VerifiedEvidenceIDs) >= 2 && len(sources) >= 2 && hasKubernetes {
			selected := *candidate
			return rootRankOutput{Selected: &selected}
		}
	}
	return rootRankOutput{NeedsAttention: true}
}
