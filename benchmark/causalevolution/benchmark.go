package causalevolution

import (
	"context"
	"strconv"

	"github.com/kubepilot-aiops/kubepilot/internal/causal/extractor"
	knowledge "github.com/kubepilot-aiops/kubepilot/internal/causal/knowledge"
	"github.com/kubepilot-aiops/kubepilot/internal/causal/validator"
	"github.com/kubepilot-aiops/kubepilot/internal/domain"
)

type Case struct {
	Incidents     []*domain.Incident
	ExpectedCause string
	ExpectedPath  []string
}
type Metrics struct {
	Cases                 int     `json:"cases"`
	ResolvedIncidents     int     `json:"resolved_incidents"`
	CausalAccuracy        float64 `json:"causal_accuracy"`
	PathCoverage          float64 `json:"path_coverage"`
	ConfidenceCalibration float64 `json:"confidence_calibration"`
}

func DefaultCases() []Case {
	return []Case{{Incidents: evolutionIncidents(100), ExpectedCause: "memory leak", ExpectedPath: []string{"memory_leak", "memory_growth", "oom_killed", "pod_restart"}}}
}

func Evaluate(cases []Case) Metrics {
	out := Metrics{Cases: len(cases)}
	for _, item := range cases {
		out.ResolvedIncidents += len(item.Incidents)
		store := knowledge.NewMemoryStore()
		v := validator.New(store)
		for _, in := range item.Incidents {
			proposal, ok := extractor.Propose(in)
			if !ok {
				continue
			}
			result, _ := v.Validate(context.Background(), in, proposal)
			if !result.Valid {
				continue
			}
			proposal.Pattern.Confidence = result.Confidence
			if result.Accepted {
				proposal.Pattern.Status = "active"
			}
			_, _ = store.Merge(context.Background(), proposal.Pattern)
		}
		patterns, _ := store.List(context.Background(), "active", 10)
		if len(patterns) == 0 {
			continue
		}
		p := patterns[0]
		if p.Cause == item.ExpectedCause {
			out.CausalAccuracy++
		}
		out.ConfidenceCalibration += 1 - abs(p.Confidence-1)
		covered := 0
		for _, expected := range item.ExpectedPath {
			for _, n := range p.CausalGraph.Nodes {
				if n.Name == expected {
					covered++
					break
				}
			}
		}
		if len(item.ExpectedPath) > 0 {
			out.PathCoverage += float64(covered) / float64(len(item.ExpectedPath))
		}
	}
	if out.Cases > 0 {
		den := float64(out.Cases)
		out.CausalAccuracy /= den
		out.PathCoverage /= den
		out.ConfidenceCalibration /= den
	}
	return out
}

func evolutionIncidents(n int) []*domain.Incident {
	out := make([]*domain.Incident, 0, n)
	for i := 1; i <= n; i++ {
		e1 := domain.Evidence{ID: "m" + strconv.Itoa(i), Source: "prometheus", Type: "memory_metric", Summary: "memory growth"}
		e2 := domain.Evidence{ID: "k" + strconv.Itoa(i), Source: "kubernetes", Type: "kubernetes_event", Summary: "OOMKilled pod restart"}
		draft := domain.HypothesisDraft{ID: "h", Cause: "memory leak", Category: "memory", PriorProbability: .95, ExpectedCausalPath: []string{"memory_leak", "memory_growth", "oom_killed", "pod_restart"}}
		verified := domain.VerifiedHypothesis{Draft: draft, VerifiedEvidenceIDs: []string{e1.ID, e2.ID}, FinalScore: .95, ContradictionScore: .01}
		out = append(out, &domain.Incident{ID: "evolution-" + strconv.Itoa(i), Namespace: "kubepilot-demo", Status: domain.StatusResolved, Evidence: []domain.Evidence{e1, e2}, DiagnosisLedger: &domain.DiagnosisLedger{SelectedHypothesisID: "h", Verified: []domain.VerifiedHypothesis{verified}}})
	}
	return out
}
func abs(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}
