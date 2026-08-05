package evaluator

import "testing"

func TestEvaluateRankingIsDeterministicAndComputesAllMetrics(t *testing.T) {
	r := EvaluateRanking([][]string{{"a", "b", "c"}}, []map[string]float64{{"b": 1}})
	if r.RecallAt5 != 1 || r.MRR != .5 || r.NDCG <= 0 || r.PrecisionAt5 != 1.0/3.0 {
		t.Fatalf("unexpected ranking metrics: %+v", r)
	}
	r2 := EvaluateRanking([][]string{{"a", "b", "c"}}, []map[string]float64{{"b": 1}})
	if r != r2 {
		t.Fatalf("scoring is not deterministic: %+v != %+v", r, r2)
	}
}
