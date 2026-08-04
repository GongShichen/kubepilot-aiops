package causal

import (
	"github.com/kubepilot-aiops/kubepilot/internal/causal"
	"github.com/kubepilot-aiops/kubepilot/internal/domain"
)

type Case struct {
	Evidence      []domain.Evidence
	ExpectedCause string
	ExpectedPath  []string
}
type Metrics struct {
	Cases              int     `json:"cases"`
	RootCauseAccuracy  float64 `json:"root_cause_accuracy"`
	CausalPathCoverage float64 `json:"causal_path_coverage"`
}

func DefaultCases() []Case {
	return []Case{{Evidence: []domain.Evidence{{Source: "prometheus", Type: "memory_metric", Summary: "memory growth"}, {Source: "kubernetes", Type: "event", Summary: "OOMKilled pod restart"}, {Source: "loki", Summary: "error rate increase"}}, ExpectedCause: "memory_leak", ExpectedPath: []string{"memory_growth", "oom_killed", "pod_restart"}}}
}

func Evaluate(matcher *causal.Matcher, cases []Case) Metrics {
	if matcher == nil {
		matcher = causal.DefaultMatcher()
	}
	metrics := Metrics{Cases: len(cases)}
	for _, item := range cases {
		matches := matcher.MatchEvidence(item.Evidence)
		if len(matches) == 0 {
			continue
		}
		best := matches[0]
		if best.Cause == item.ExpectedCause {
			metrics.RootCauseAccuracy++
		}
		if len(item.ExpectedPath) > 0 {
			found := map[string]bool{}
			for _, node := range best.CausalPath {
				found[node] = true
			}
			covered := 0
			for _, node := range item.ExpectedPath {
				if found[node] {
					covered++
				}
			}
			metrics.CausalPathCoverage += float64(covered) / float64(len(item.ExpectedPath))
		}
	}
	if metrics.Cases > 0 {
		metrics.RootCauseAccuracy /= float64(metrics.Cases)
		metrics.CausalPathCoverage /= float64(metrics.Cases)
	}
	return metrics
}
