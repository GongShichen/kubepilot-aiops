// Deprecated: use benchmark/incident_retrieval. Package incident contains
// compatibility metrics and ablation definitions for historical incident
// retrieval. It is separate from the log/template benchmark.
package incident

import "math"

type Metrics struct {
	Strategy   string  `json:"strategy"`
	RecallAt1  float64 `json:"recall_at_1"`
	RecallAt5  float64 `json:"recall_at_5"`
	RecallAt10 float64 `json:"recall_at_10"`
	MRR        float64 `json:"mrr"`
	NDCG       float64 `json:"ndcg"`
}

var AblationStrategies = []string{
	"baseline",
	"semantic",
	"semantic_lexical",
	"semantic_lexical_topology",
	"semantic_lexical_topology_causal",
	"full_neural",
}

func Evaluate(strategy string, ranks []int) Metrics {
	out := Metrics{Strategy: strategy}
	if len(ranks) == 0 {
		return out
	}
	for _, rank := range ranks {
		if rank == 1 {
			out.RecallAt1++
		}
		if rank > 0 && rank <= 5 {
			out.RecallAt5++
		}
		if rank > 0 && rank <= 10 {
			out.RecallAt10++
		}
		if rank > 0 {
			out.MRR += 1 / float64(rank)
			// Binary relevance is appropriate for the incident ground truth;
			// this is the same logarithmic gain used by the main evaluator.
			out.NDCG += 1 / log2(float64(rank+1))
		}
	}
	n := float64(len(ranks))
	out.RecallAt1 /= n
	out.RecallAt5 /= n
	out.RecallAt10 /= n
	out.MRR /= n
	out.NDCG /= n
	return out
}

func log2(value float64) float64 {
	// log2(x) = ln(x)/ln(2); keeping this tiny helper local avoids coupling
	// benchmark metric packages to the runtime retrieval implementation.
	return math.Log(value) / 0.6931471805599453
}
