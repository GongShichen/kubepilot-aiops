// Package evolution exposes evaluator-only scores for the knowledge evolution
// stage. Production learning remains behind its existing extractor/validator
// boundary; this package never writes to the production knowledge store.
package evolution

type PatternCase struct {
	Expected   []string
	Found      []string
	Confidence float64
}
type Metrics struct {
	Cases                 int     `json:"cases"`
	PatternPrecision      float64 `json:"pattern_precision"`
	PatternRecall         float64 `json:"pattern_recall"`
	ConfidenceCalibration float64 `json:"confidence_calibration"`
}

func Evaluate(cases []PatternCase) Metrics {
	out := Metrics{Cases: len(cases)}
	if len(cases) == 0 {
		return out
	}
	for _, c := range cases {
		es := map[string]bool{}
		fs := map[string]bool{}
		for _, v := range c.Expected {
			es[v] = true
		}
		for _, v := range c.Found {
			fs[v] = true
		}
		tp := 0
		for v := range fs {
			if es[v] {
				tp++
			}
		}
		if len(fs) > 0 {
			out.PatternPrecision += float64(tp) / float64(len(fs))
		}
		if len(es) > 0 {
			out.PatternRecall += float64(tp) / float64(len(es))
		}
		if len(fs) > 0 {
			p := float64(tp) / float64(len(fs))
			d := c.Confidence - p
			if d < 0 {
				d = -d
			}
			out.ConfidenceCalibration += 1 - d
		}
	}
	d := float64(len(cases))
	out.PatternPrecision /= d
	out.PatternRecall /= d
	out.ConfidenceCalibration /= d
	return out
}
