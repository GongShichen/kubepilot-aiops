package diagnosis

import (
	"testing"

	"github.com/kubepilot-aiops/kubepilot/benchmark/evaluator"
	incidentbench "github.com/kubepilot-aiops/kubepilot/benchmark/incident"
)

func TestDiagnosisMetricsSeparateFromLogMetrics(t *testing.T) {
	result := incidentbench.CaseResult{Score: evaluator.Score{RootCauseCorrect: true, EvidencePrecision: 1}, Observation: incidentbench.Observation{ToolCalls: 2, Hypotheses: []evaluator.Hypothesis{{Supported: true}}}}
	metrics := Evaluate([]Result{result})
	if metrics.RCAAccuracy != 1 || metrics.EvidenceAttribution != 1 || metrics.HypothesisQuality != 1 {
		t.Fatalf("unexpected diagnosis metrics: %+v", metrics)
	}
}
