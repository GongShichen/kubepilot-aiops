// Deprecated: use benchmark/log_retrieval. Package log contains metrics for
// the legacy log/template retrieval benchmark. It
// is intentionally independent from incident retrieval so a template miss
// cannot be mistaken for an incident-ranking miss.
package log

import "sort"

type Metrics struct {
	RecallAt1  float64 `json:"recall_at_1"`
	RecallAt5  float64 `json:"recall_at_5"`
	RecallAt10 float64 `json:"recall_at_10"`
	MRR        float64 `json:"mrr"`
}

// Evaluate consumes one ranked template list per query and its expected
// template.  The input is deliberately generic so the log benchmark can be
// backed by Loki, Drain3, or a template index without coupling those systems.
func Evaluate(ranked [][]string, expected []string) Metrics {
	if len(ranked) == 0 || len(ranked) != len(expected) {
		return Metrics{}
	}
	var out Metrics
	for i, items := range ranked {
		rank := 0
		for j, item := range items {
			if item == expected[i] {
				rank = j + 1
				break
			}
		}
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
		}
	}
	n := float64(len(ranked))
	out.RecallAt1 /= n
	out.RecallAt5 /= n
	out.RecallAt10 /= n
	out.MRR /= n
	return out
}

func Stable(ids []string) []string {
	out := append([]string(nil), ids...)
	sort.Strings(out)
	return out
}
