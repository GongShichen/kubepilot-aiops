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
	Negative     bool
	Distribution string
}

type Metrics struct {
	Cases                 int     `json:"cases"`
	PatternPrecision      float64 `json:"pattern_precision"`
	PatternRecall         float64 `json:"pattern_recall"`
	PatternF1             float64 `json:"pattern_f1"`
	FalseDiscoveryRate    float64 `json:"false_discovery_rate"`
	MeanPathEditDistance  float64 `json:"mean_path_edit_distance"`
	ConfidenceCalibration float64 `json:"confidence_calibration"`
	DiscoveredPatterns    int     `json:"discovered_patterns"`
}

// DefaultCases is an offline discovery benchmark: it uses one deterministic
// corpus of resolved incidents and never participates in production
// knowledge learning.
func DefaultCases() []Case {
	return []Case{
		{Incidents: resolvedIncidents("memory", 30, "memory leak", []string{"memory_growth", "oom_killed", "pod_restart"}), ExpectedPath: []string{"memory leak", "memory_growth", "oom_killed", "pod_restart"}, Distribution: "in_distribution"},
		{Incidents: resolvedIncidents("database", 30, "connection saturation", []string{"connection_wait", "request_timeout", "payment_error"}), ExpectedPath: []string{"connection saturation", "connection_wait", "request_timeout", "payment_error"}, Distribution: "in_distribution"},
		{Incidents: resolvedIncidents("network", 30, "selector mismatch", []string{"missing_endpoint", "downstream_failure", "gateway_error"}), ExpectedPath: []string{"selector mismatch", "missing_endpoint", "downstream_failure", "gateway_error"}, Distribution: "in_distribution"},
		{Incidents: resolvedIncidents("memory-variant", 30, "retained object leak", []string{"heap_growth", "container_termination", "replica_churn"}), ExpectedPath: []string{"retained object leak", "heap_growth", "container_termination", "replica_churn"}, Distribution: "out_of_distribution"},
		{Incidents: heterogeneousIncidents(30), Negative: true, Distribution: "negative"},
	}
}

func Evaluate(cases []Case) Metrics {
	metrics := Metrics{}
	truePositive, falsePositive, falseNegative := 0, 0, 0
	pathDistanceTotal, pathDistanceCount := 0.0, 0
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
		bestDistance := 1.0
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
			if !item.Negative {
				distance := normalizedEditDistance(candidate.CausalPath, item.ExpectedPath)
				if distance < bestDistance {
					bestDistance = distance
				}
			}
		}
		metrics.DiscoveredPatterns += accepted
		if item.Negative {
			falsePositive += accepted
			if accepted > 0 {
				metrics.ConfidenceCalibration += 1 - confidence/float64(accepted)
			}
			continue
		}
		if accepted > 0 {
			if exact {
				truePositive++
			} else {
				falsePositive += accepted
			}
			metrics.ConfidenceCalibration += 1 - abs(confidence/float64(accepted)-1)
			pathDistanceTotal += bestDistance
			pathDistanceCount++
		} else {
			falseNegative++
		}
	}
	if truePositive+falsePositive > 0 {
		metrics.PatternPrecision = float64(truePositive) / float64(truePositive+falsePositive)
		metrics.FalseDiscoveryRate = float64(falsePositive) / float64(truePositive+falsePositive)
	}
	if truePositive+falseNegative > 0 {
		metrics.PatternRecall = float64(truePositive) / float64(truePositive+falseNegative)
	}
	if metrics.PatternPrecision+metrics.PatternRecall > 0 {
		metrics.PatternF1 = 2 * metrics.PatternPrecision * metrics.PatternRecall / (metrics.PatternPrecision + metrics.PatternRecall)
	}
	if pathDistanceCount > 0 {
		metrics.MeanPathEditDistance = pathDistanceTotal / float64(pathDistanceCount)
	}
	if len(cases) > 0 {
		metrics.ConfidenceCalibration /= float64(len(cases))
	}
	return metrics
}

func resolvedIncidents(prefix string, count int, cause string, path []string) []*domain.Incident {
	incidents := make([]*domain.Incident, 0, count)
	for i := 0; i < count; i++ {
		id := "discovery-" + prefix + "-" + strconv.Itoa(i)
		metric := id + "-metric"
		event := id + "-event"
		incidents = append(incidents, &domain.Incident{
			ID: id, Status: domain.StatusResolved, Namespace: "kubepilot-demo", Service: "payment-service",
			Evidence:             []domain.Evidence{{ID: metric, Source: "prometheus", Type: "metric", Summary: "memory growth", Confidence: .9}, {ID: event, Source: "kubernetes", Type: "kubernetes_event", Summary: "OOMKilled", Confidence: .95}},
			Verification:         &domain.Verification{Success: true, Checks: map[string]bool{"ready": true, "probe": true, "errors": true}},
			Confidence:           .9,
			RootCauseEvidenceIDs: []string{metric, event},
			DiagnosisLedger:      &domain.DiagnosisLedger{SelectedHypothesisID: "h", Verified: []domain.VerifiedHypothesis{{Draft: domain.HypothesisDraft{ID: "h", Cause: cause, ExpectedCausalPath: append([]string(nil), path...), SupportingEvidenceIDs: []string{metric, event}}, FinalScore: .9, Status: domain.HypothesisSupported}}},
		})
	}
	return incidents
}

func heterogeneousIncidents(count int) []*domain.Incident {
	out := make([]*domain.Incident, 0, count)
	for index := 0; index < count; index++ {
		cause := "unrelated cause " + strconv.Itoa(index)
		path := []string{"isolated observation " + strconv.Itoa(index), "unrelated outcome " + strconv.Itoa(index)}
		out = append(out, resolvedIncidents("negative-"+strconv.Itoa(index), 1, cause, path)...)
	}
	return out
}

func normalizedEditDistance(left, right []string) float64 {
	if len(left) == 0 && len(right) == 0 {
		return 0
	}
	previous := make([]int, len(right)+1)
	for index := range previous {
		previous[index] = index
	}
	for row, leftValue := range left {
		current := make([]int, len(right)+1)
		current[0] = row + 1
		for column, rightValue := range right {
			cost := 1
			if leftValue == rightValue {
				cost = 0
			}
			current[column+1] = min(current[column]+1, previous[column+1]+1, previous[column]+cost)
		}
		previous = current
	}
	return float64(previous[len(right)]) / float64(max(len(left), len(right)))
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
