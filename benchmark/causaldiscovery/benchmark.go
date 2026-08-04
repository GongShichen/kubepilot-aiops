package causaldiscovery

import (
	"context"
	"strconv"

	discovery "github.com/kubepilot-aiops/kubepilot/internal/causal/discovery"
	"github.com/kubepilot-aiops/kubepilot/internal/domain"
)

type Case struct {
	Incidents    []*domain.Incident
	ExpectedPath []string
}

type Metrics struct {
	Cases                 int     `json:"cases"`
	PatternPrecision      float64 `json:"pattern_precision"`
	PatternRecall         float64 `json:"pattern_recall"`
	ConfidenceCalibration float64 `json:"confidence_calibration"`
	DiscoveredPatterns    int     `json:"discovered_patterns"`
}

// DefaultCases is an offline discovery benchmark: it uses one deterministic
// corpus of 100 resolved incidents and never participates in production
// knowledge learning.
func DefaultCases() []Case {
	return []Case{{Incidents: resolvedIncidents(100), ExpectedPath: []string{"memory leak", "memory_growth", "oom_killed", "pod_restart"}}}
}

func Evaluate(cases []Case) Metrics {
	metrics := Metrics{}
	for _, item := range cases {
		metrics.Cases += len(item.Incidents)
		candidateStore := discovery.NewMemoryStore()
		engine := discovery.NewEngine(candidateStore, nil)
		candidates, err := engine.Discover(context.Background(), item.Incidents)
		if err != nil {
			continue
		}
		accepted := 0
		exact := false
		confidence := 0.0
		for _, candidate := range candidates {
			if candidate.Status != discovery.StatusAccepted {
				continue
			}
			accepted++
			confidence += candidate.Confidence
			if equalPath(candidate.CausalPath, item.ExpectedPath) {
				exact = true
			}
		}
		metrics.DiscoveredPatterns += accepted
		if accepted > 0 {
			metrics.PatternPrecision += boolFloat(exact) / float64(accepted)
			metrics.ConfidenceCalibration += 1 - abs(confidence/float64(accepted)-1)
		}
		metrics.PatternRecall += boolFloat(exact)
	}
	if len(cases) > 0 {
		den := float64(len(cases))
		metrics.PatternPrecision /= den
		metrics.PatternRecall /= den
		metrics.ConfidenceCalibration /= den
	}
	return metrics
}

func resolvedIncidents(count int) []*domain.Incident {
	incidents := make([]*domain.Incident, 0, count)
	for i := 0; i < count; i++ {
		id := "discovery-" + strconv.Itoa(i)
		metric := id + "-metric"
		event := id + "-event"
		incidents = append(incidents, &domain.Incident{
			ID: id, Status: domain.StatusResolved, Namespace: "kubepilot-demo", Service: "payment-service",
			Evidence:             []domain.Evidence{{ID: metric, Source: "prometheus", Type: "metric", Summary: "memory growth", Confidence: .9}, {ID: event, Source: "kubernetes", Type: "kubernetes_event", Summary: "OOMKilled", Confidence: .95}},
			Verification:         &domain.Verification{Success: true, Checks: map[string]bool{"ready": true, "probe": true, "errors": true}},
			Confidence:           .9,
			RootCauseEvidenceIDs: []string{metric, event},
			DiagnosisLedger:      &domain.DiagnosisLedger{SelectedHypothesisID: "h", Verified: []domain.VerifiedHypothesis{{Draft: domain.HypothesisDraft{ID: "h", Cause: "memory leak", ExpectedCausalPath: []string{"memory_growth", "oom_killed", "pod_restart"}, SupportingEvidenceIDs: []string{metric, event}}, FinalScore: .9, Status: domain.HypothesisSupported}}},
		})
	}
	return incidents
}

func equalPath(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}
func boolFloat(value bool) float64 {
	if value {
		return 1
	}
	return 0
}
func abs(value float64) float64 {
	if value < 0 {
		return -value
	}
	return value
}
