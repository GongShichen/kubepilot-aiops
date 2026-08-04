package causal

type Case struct {
	Evidence       []string
	PredictedCause string
	PredictedPath  []string
	ExpectedCause  string
	ExpectedPath   []string
}
type Metrics struct {
	Cases            int     `json:"cases"`
	CausalAccuracy   float64 `json:"causal_accuracy"`
	PathCoverage     float64 `json:"path_coverage"`
	PatternPrecision float64 `json:"pattern_precision"`
	PatternRecall    float64 `json:"pattern_recall"`
}

func Evaluate(cases []Case) Metrics {
	out := Metrics{Cases: len(cases)}
	for _, c := range cases {
		if c.PredictedCause == c.ExpectedCause {
			out.CausalAccuracy++
		}
		out.PathCoverage += coverage(c.PredictedPath, c.ExpectedPath)
	}
	if len(cases) > 0 {
		d := float64(len(cases))
		out.CausalAccuracy /= d
		out.PathCoverage /= d
	}
	out.PatternPrecision = out.CausalAccuracy
	out.PatternRecall = out.CausalAccuracy
	return out
}
func coverage(actual, expected []string) float64 {
	if len(expected) == 0 {
		return 0
	}
	s := map[string]bool{}
	for _, v := range actual {
		s[v] = true
	}
	n := 0
	for _, v := range expected {
		if s[v] {
			n++
		}
	}
	return float64(n) / float64(len(expected))
}
