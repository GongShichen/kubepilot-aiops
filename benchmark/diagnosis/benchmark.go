// Package diagnosis is the end-to-end diagnosis benchmark. It measures the
// public Incident API and keeps scoring separate from log and incident
// retrieval metrics.
package diagnosis

import (
	"context"

	"github.com/kubepilot-aiops/kubepilot/benchmark/evaluator"
	incidentbench "github.com/kubepilot-aiops/kubepilot/benchmark/incident"
)

type Case = incidentbench.Case
type Result = incidentbench.CaseResult
type Agent = incidentbench.PublicAgent

type Config = incidentbench.Config

func Run(ctx context.Context, agent Agent, cases []Case, config Config) []Result {
	return (incidentbench.Runner{Agent: agent, Config: config}).Run(ctx, cases)
}

type Metrics struct {
	Cases               int     `json:"cases"`
	RCAAccuracy         float64 `json:"rca_accuracy"`
	EvidenceAttribution float64 `json:"evidence_attribution"`
	HypothesisQuality   float64 `json:"hypothesis_quality"`
	CausalPathCoverage  float64 `json:"causal_path_coverage"`
	ToolEfficiency      float64 `json:"tool_efficiency"`
	MeanToolCalls       float64 `json:"mean_tool_calls"`
	MeanIterations      float64 `json:"mean_iterations"`
	MeanCorrections     float64 `json:"mean_corrections"`
	MeanTokens          float64 `json:"mean_tokens"`
}

func Evaluate(results []Result) Metrics {
	metrics := Metrics{Cases: len(results)}
	if len(results) == 0 {
		return metrics
	}
	for _, result := range results {
		metrics.RCAAccuracy += boolFloat(result.Score.RootCauseCorrect)
		metrics.EvidenceAttribution += result.Score.EvidencePrecision
		metrics.MeanToolCalls += float64(result.Observation.ToolCalls)
		metrics.MeanIterations += float64(result.Observation.Iterations)
		metrics.MeanCorrections += float64(result.Observation.Corrections)
		metrics.MeanTokens += float64(result.Observation.Tokens)
		if len(result.Observation.Hypotheses) == 0 {
			continue
		}
		correct, total := 0, 0
		for _, hypothesis := range result.Observation.Hypotheses {
			total++
			if hypothesis.Supported || hypothesis.Verified {
				correct++
			}
		}
		metrics.HypothesisQuality += float64(correct) / float64(total)
	}
	denominator := float64(len(results))
	metrics.RCAAccuracy /= denominator
	metrics.EvidenceAttribution /= denominator
	metrics.HypothesisQuality /= denominator
	metrics.CausalPathCoverage /= denominator
	metrics.MeanToolCalls /= denominator
	metrics.MeanIterations /= denominator
	metrics.MeanCorrections /= denominator
	metrics.MeanTokens /= denominator
	metrics.ToolEfficiency = 1 / (1 + metrics.MeanToolCalls)
	return metrics
}

func (m Metrics) Score() evaluator.DiagnosisMetrics {
	return evaluator.DiagnosisMetrics{Cases: m.Cases, RootCauseAccuracy: m.RCAAccuracy, EvidenceAttribution: m.EvidenceAttribution, MeanDiagnosisMS: 0, MeanEvidenceQueries: 0}
}

func boolFloat(value bool) float64 {
	if value {
		return 1
	}
	return 0
}
