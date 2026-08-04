// Package evaluator contains deterministic, evaluator-side scoring primitives.
// It intentionally has no dependency on the Agent runtime. Expected labels are
// accepted only by these functions and are never part of an agent input.
package evaluator

import (
	"math"
	"sort"
)

// Evaluator is the common contract for benchmark evaluators. Implementations
// must be deterministic for the same result and must not mutate it.
type Evaluator interface {
	Name() string
	Evaluate(IncidentResult) Score
}

// IncidentResult is the evaluator-side result envelope. Input is the
// observation sent to the public Agent API; Expected is deliberately separate
// and must never be serialized into an Agent request.
type IncidentResult struct {
	CaseID   string         `json:"case_id"`
	Input    map[string]any `json:"input,omitempty"`
	Expected Expected       `json:"expected,omitempty"`
	Observed Observed       `json:"observed"`
}

type Expected struct {
	RootCause       string
	Category        string
	Service         string
	Resource        string
	EvidenceIDs     []string
	CausalPath      []string
	RecoveryAction  string
	RecoveryTarget  string
	AllowedPatterns []string
}

type Observed struct {
	RootCause        string
	Category         string
	Service          string
	Resource         string
	EvidenceIDs      []string
	Hypotheses       []Hypothesis
	RecoveryAction   string
	RecoveryTarget   string
	VerificationOK   bool
	Status           string
	ToolCalls        int
	ToolCost         int
	Iterations       int
	Tokens           int
	Corrections      int
	SafetyRejections int
	DurationMS       float64
	EvidenceQueries  int
}

type Hypothesis struct {
	ID         string
	Cause      string
	Supported  bool
	Verified   bool
	EvidenceID []string
}

type Score struct {
	RootCauseCorrect    bool    `json:"root_cause_correct"`
	CategoryCorrect     bool    `json:"category_correct"`
	ServiceCorrect      bool    `json:"service_correct"`
	ResourceCorrect     bool    `json:"resource_correct"`
	EvidencePrecision   float64 `json:"evidence_precision"`
	EvidenceRecall      float64 `json:"evidence_recall"`
	CausalPathCoverage  float64 `json:"causal_path_coverage"`
	HypothesisRecall    float64 `json:"hypothesis_recall"`
	FalseHypothesisRate float64 `json:"false_hypothesis_rate"`
	RecoveryCorrect     bool    `json:"recovery_correct"`
	VerificationSuccess bool    `json:"verification_success"`
	SafetyViolation     bool    `json:"safety_violation"`
	DiagnosisDurationMS float64 `json:"diagnosis_duration_ms"`
	RecoveryDurationMS  float64 `json:"recovery_duration_ms"`
}

// RankingMetrics computes binary/graded retrieval metrics. Relevant values
// may be 0/1 or graded relevance; the expected set remains evaluator-only.
type RankingMetrics struct {
	Queries       int     `json:"queries"`
	RecallAt1     float64 `json:"recall_at_1"`
	RecallAt5     float64 `json:"recall_at_5"`
	RecallAt10    float64 `json:"recall_at_10"`
	PrecisionAt1  float64 `json:"precision_at_1"`
	PrecisionAt5  float64 `json:"precision_at_5"`
	PrecisionAt10 float64 `json:"precision_at_10"`
	MRR           float64 `json:"mrr"`
	NDCG          float64 `json:"ndcg"`
}

func EvaluateRanking(rankings [][]string, relevant []map[string]float64) RankingMetrics {
	out := RankingMetrics{Queries: len(rankings)}
	if len(rankings) == 0 {
		return out
	}
	for i, ranking := range rankings {
		if i >= len(relevant) {
			break
		}
		r := relevant[i]
		if hit(ranking, r, 1) {
			out.RecallAt1++
		}
		if hit(ranking, r, 5) {
			out.RecallAt5++
		}
		if hit(ranking, r, 10) {
			out.RecallAt10++
		}
		out.PrecisionAt1 += precision(ranking, r, 1)
		out.PrecisionAt5 += precision(ranking, r, 5)
		out.PrecisionAt10 += precision(ranking, r, 10)
		for pos, id := range ranking {
			if r[id] > 0 {
				out.MRR += 1 / float64(pos+1)
				break
			}
		}
		out.NDCG += ndcg(ranking, r)
	}
	den := float64(len(rankings))
	out.RecallAt1 /= den
	out.RecallAt5 /= den
	out.RecallAt10 /= den
	out.PrecisionAt1 /= den
	out.PrecisionAt5 /= den
	out.PrecisionAt10 /= den
	out.MRR /= den
	out.NDCG /= den
	return out
}

func hit(ids []string, rel map[string]float64, k int) bool {
	if k > len(ids) {
		k = len(ids)
	}
	for _, id := range ids[:k] {
		if rel[id] > 0 {
			return true
		}
	}
	return false
}
func precision(ids []string, rel map[string]float64, k int) float64 {
	if k > len(ids) {
		k = len(ids)
	}
	if k == 0 {
		return 0
	}
	n := 0
	for _, id := range ids[:k] {
		if rel[id] > 0 {
			n++
		}
	}
	return float64(n) / float64(k)
}
func ndcg(ids []string, rel map[string]float64) float64 {
	if len(ids) == 0 || len(rel) == 0 {
		return 0
	}
	dcg := 0.0
	for i, id := range ids {
		if rel[id] > 0 {
			dcg += (math.Pow(2, rel[id]) - 1) / math.Log2(float64(i+2))
		}
	}
	ideal := make([]float64, 0, len(rel))
	for _, v := range rel {
		if v > 0 {
			ideal = append(ideal, v)
		}
	}
	sort.Sort(sort.Reverse(sort.Float64Slice(ideal)))
	idcg := 0.0
	for i, v := range ideal {
		idcg += (math.Pow(2, v) - 1) / math.Log2(float64(i+2))
	}
	if idcg == 0 {
		return 0
	}
	return dcg / idcg
}

type DiagnosisMetrics struct {
	Cases               int     `json:"cases"`
	RootCauseAccuracy   float64 `json:"root_cause_accuracy"`
	EvidenceAttribution float64 `json:"evidence_attribution_accuracy"`
	HypothesisRecall    float64 `json:"hypothesis_recall"`
	FalseHypothesisRate float64 `json:"false_hypothesis_rate"`
	MeanDiagnosisMS     float64 `json:"mean_diagnosis_ms"`
	MeanEvidenceQueries float64 `json:"mean_evidence_queries"`
}

func EvaluateDiagnosis(results []IncidentResult) DiagnosisMetrics {
	out := DiagnosisMetrics{Cases: len(results)}
	if len(results) == 0 {
		return out
	}
	for _, r := range results {
		if r.Observed.RootCause != "" && r.Observed.RootCause == r.Expected.RootCause {
			out.RootCauseAccuracy++
		}
		if len(r.Expected.EvidenceIDs) > 0 {
			out.EvidenceAttribution += overlap(r.Observed.EvidenceIDs, r.Expected.EvidenceIDs)
		}
		correctHyp, falseHyp := 0, 0
		for _, h := range r.Observed.Hypotheses {
			if h.Cause == r.Expected.RootCause {
				correctHyp++
			} else {
				falseHyp++
			}
		}
		if correctHyp > 0 {
			out.HypothesisRecall++
		}
		if len(r.Observed.Hypotheses) > 0 {
			out.FalseHypothesisRate += float64(falseHyp) / float64(len(r.Observed.Hypotheses))
		}
		out.MeanDiagnosisMS += r.Observed.DurationMS
		out.MeanEvidenceQueries += float64(r.Observed.EvidenceQueries)
	}
	den := float64(len(results))
	out.RootCauseAccuracy /= den
	out.EvidenceAttribution /= den
	out.HypothesisRecall /= den
	out.FalseHypothesisRate /= den
	out.MeanDiagnosisMS /= den
	out.MeanEvidenceQueries /= den
	return out
}

func overlap(actual, expected []string) float64 {
	set := map[string]bool{}
	for _, id := range expected {
		set[id] = true
	}
	if len(set) == 0 {
		return 0
	}
	n := 0
	seen := map[string]bool{}
	for _, id := range actual {
		if set[id] && !seen[id] {
			n++
			seen[id] = true
		}
	}
	return float64(n) / float64(len(set))
}

type AgentMetrics struct {
	Cases                 int     `json:"cases"`
	AverageToolCalls      float64 `json:"average_tool_calls"`
	AverageToolCost       float64 `json:"average_tool_cost"`
	AverageIterations     float64 `json:"average_iterations"`
	AverageCorrections    float64 `json:"average_corrections"`
	BudgetExhaustRate     float64 `json:"budget_exhaust_rate"`
	CorrectionSuccessRate float64 `json:"correction_success_rate"`
}

func EvaluateAgent(results []IncidentResult) AgentMetrics {
	o := AgentMetrics{Cases: len(results)}
	if len(results) == 0 {
		return o
	}
	exhausted, corrected, successes := 0, 0, 0
	for _, r := range results {
		o.AverageToolCalls += float64(r.Observed.ToolCalls)
		o.AverageToolCost += float64(r.Observed.ToolCost)
		o.AverageIterations += float64(r.Observed.Iterations)
		o.AverageCorrections += float64(r.Observed.Corrections)
		if r.Observed.Status == "BUDGET_EXHAUSTED" {
			exhausted++
		}
		if r.Observed.Corrections > 0 {
			corrected++
			if r.Observed.RootCause == r.Expected.RootCause {
				successes++
			}
		}
	}
	d := float64(len(results))
	o.AverageToolCalls /= d
	o.AverageToolCost /= d
	o.AverageIterations /= d
	o.AverageCorrections /= d
	o.BudgetExhaustRate = float64(exhausted) / d
	if corrected > 0 {
		o.CorrectionSuccessRate = float64(successes) / float64(corrected)
	}
	return o
}

type RecoveryMetrics struct {
	Cases               int     `json:"cases"`
	Safety              int     `json:"safety"`
	RecoveryAccuracy    float64 `json:"recovery_accuracy"`
	VerificationSuccess float64 `json:"verification_success"`
	MeanRecoveryMS      float64 `json:"mean_recovery_ms"`
}

func EvaluateRecovery(results []IncidentResult) RecoveryMetrics {
	o := RecoveryMetrics{Cases: len(results)}
	if len(results) == 0 {
		return o
	}
	for _, r := range results {
		if r.Observed.RecoveryAction != "" && r.Observed.RecoveryAction == r.Expected.RecoveryAction && r.Observed.RecoveryTarget == r.Expected.RecoveryTarget {
			o.RecoveryAccuracy++
		}
		if r.Observed.VerificationOK {
			o.VerificationSuccess++
		}
		if r.Observed.Status == "SAFETY_REJECTED" {
			o.Safety++
		}
		o.MeanRecoveryMS += r.Observed.DurationMS
	}
	d := float64(len(results))
	o.RecoveryAccuracy /= d
	o.VerificationSuccess /= d
	o.MeanRecoveryMS /= d
	return o
}

type EvolutionMetrics struct {
	Cases                 int     `json:"cases"`
	PatternPrecision      float64 `json:"pattern_precision"`
	PatternRecall         float64 `json:"pattern_recall"`
	ConfidenceCalibration float64 `json:"confidence_calibration"`
}

func EvaluateEvolution(found, expected []string, confidence float64) EvolutionMetrics {
	o := EvolutionMetrics{Cases: 1}
	if len(found) == 0 {
		return o
	}
	fs := map[string]bool{}
	es := map[string]bool{}
	for _, v := range found {
		fs[v] = true
	}
	for _, v := range expected {
		es[v] = true
	}
	tp := 0
	for v := range fs {
		if es[v] {
			tp++
		}
	}
	o.PatternPrecision = float64(tp) / float64(len(fs))
	if len(es) > 0 {
		o.PatternRecall = float64(tp) / float64(len(es))
	}
	if confidence < 0 {
		confidence = 0
	}
	if confidence > 1 {
		confidence = 1
	}
	if o.PatternPrecision > 0 {
		o.ConfidenceCalibration = 1 - math.Abs(confidence-o.PatternPrecision)
	}
	return o
}
