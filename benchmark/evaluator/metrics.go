// Package evaluator contains deterministic, evaluator-side scoring primitives.
// It intentionally has no dependency on the Agent runtime.
package evaluator

import (
	"math"
	"sort"
)

// RankingMetrics computes binary/graded retrieval metrics. Relevant values
// may be 0/1 or graded relevance; the expected set remains evaluator-only.
type RankingMetrics struct {
	Queries       int     `json:"queries"`
	RecallAt1     float64 `json:"recall_at_1"`
	RecallAt5     float64 `json:"recall_at_5"`
	RecallAt10    float64 `json:"recall_at_10"`
	PrecisionAt1  float64 `json:"precision_at_1"`
	PrecisionAt5  float64 `json:"precision_at_5"`
	PrecisionAt10 float64 `json:"precision_at_10"`
	MRR           float64 `json:"mrr"`
	NDCG          float64 `json:"ndcg"`
}

func EvaluateRanking(rankings [][]string, relevant []map[string]float64) RankingMetrics {
	out := RankingMetrics{Queries: len(rankings)}
	if len(rankings) == 0 {
		return out
	}
	for i, ranking := range rankings {
		if i >= len(relevant) {
			break
		}
		r := relevant[i]
		if hit(ranking, r, 1) {
			out.RecallAt1++
		}
		if hit(ranking, r, 5) {
			out.RecallAt5++
		}
		if hit(ranking, r, 10) {
			out.RecallAt10++
		}
		out.PrecisionAt1 += precision(ranking, r, 1)
		out.PrecisionAt5 += precision(ranking, r, 5)
		out.PrecisionAt10 += precision(ranking, r, 10)
		for pos, id := range ranking {
			if r[id] > 0 {
				out.MRR += 1 / float64(pos+1)
				break
			}
		}
		out.NDCG += ndcg(ranking, r)
	}
	den := float64(len(rankings))
	out.RecallAt1 /= den
	out.RecallAt5 /= den
	out.RecallAt10 /= den
	out.PrecisionAt1 /= den
	out.PrecisionAt5 /= den
	out.PrecisionAt10 /= den
	out.MRR /= den
	out.NDCG /= den
	return out
}

func hit(ids []string, rel map[string]float64, k int) bool {
	if k > len(ids) {
		k = len(ids)
	}
	for _, id := range ids[:k] {
		if rel[id] > 0 {
			return true
		}
	}
	return false
}
func precision(ids []string, rel map[string]float64, k int) float64 {
	if k > len(ids) {
		k = len(ids)
	}
	if k == 0 {
		return 0
	}
	n := 0
	for _, id := range ids[:k] {
		if rel[id] > 0 {
			n++
		}
	}
	return float64(n) / float64(k)
}
func ndcg(ids []string, rel map[string]float64) float64 {
	if len(ids) == 0 || len(rel) == 0 {
		return 0
	}
	dcg := 0.0
	for i, id := range ids {
		if rel[id] > 0 {
			dcg += (math.Pow(2, rel[id]) - 1) / math.Log2(float64(i+2))
		}
	}
	ideal := make([]float64, 0, len(rel))
	for _, v := range rel {
		if v > 0 {
			ideal = append(ideal, v)
		}
	}
	sort.Sort(sort.Reverse(sort.Float64Slice(ideal)))
	idcg := 0.0
	for i, v := range ideal {
		idcg += (math.Pow(2, v) - 1) / math.Log2(float64(i+2))
	}
	if idcg == 0 {
		return 0
	}
	return dcg / idcg
}
