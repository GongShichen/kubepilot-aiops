package incident

import "testing"

func TestAblationStrategiesExposeReasoningStages(t *testing.T) {
	if len(AblationStrategies) != 6 || AblationStrategies[2] != "semantic_lexical" || AblationStrategies[5] != "full_neural" {
		t.Fatalf("strategies=%v", AblationStrategies)
	}
	got := Evaluate("full_neural", []int{1, 3, 0})
	if got.RecallAt5 != 2.0/3.0 || got.MRR <= .4 || got.NDCG < .5 {
		t.Fatalf("metrics=%+v", got)
	}
}
