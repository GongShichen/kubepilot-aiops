package reporter

import (
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/kubepilot-aiops/kubepilot/benchmark/scorer"
)

func TestConfidenceCalibration(t *testing.T) {
	items := []CaseResult{
		{Confidence: .9, Score: scorer.Score{RootCauseCorrect: true}},
		{Confidence: .8, Score: scorer.Score{RootCauseCorrect: false}},
	}
	brier, ece, highError := confidenceCalibration(items)
	if math.Abs(brier-.325) > .0001 || math.Abs(ece-.45) > .0001 || highError != .5 {
		t.Fatalf("brier=%f ece=%f highError=%f", brier, ece, highError)
	}
}

func TestCategoryMacroF1(t *testing.T) {
	items := []CaseResult{
		{Category: "cpu", RootCauseCategory: "cpu"},
		{Category: "memory", RootCauseCategory: "cpu"},
	}
	got := categoryMacroF1(items)
	// CPU F1 is 2/3 and the other five categories score zero.
	if math.Abs(got-(2.0/3.0)/6.0) > .0001 {
		t.Fatalf("macro F1=%f", got)
	}
}

func TestCategoryMetrics(t *testing.T) {
	items := []CaseResult{
		{Category: "cpu", RootCauseCategory: "cpu"},
		{Category: "cpu", RootCauseCategory: "memory"},
		{Category: "memory", RootCauseCategory: "cpu"},
	}
	metrics := categoryMetrics(items)
	if len(metrics) != 6 {
		t.Fatalf("metrics=%d", len(metrics))
	}
	cpu := metrics[0]
	if cpu.Support != 2 || cpu.TruePositive != 1 || cpu.FalsePositive != 1 || cpu.FalseNegative != 1 {
		t.Fatalf("unexpected cpu metric: %+v", cpu)
	}
	if math.Abs(cpu.Precision-.5) > .0001 || math.Abs(cpu.Recall-.5) > .0001 || math.Abs(cpu.F1-.5) > .0001 {
		t.Fatalf("unexpected cpu rates: %+v", cpu)
	}
}

func TestCalibrationBins(t *testing.T) {
	items := []CaseResult{
		{Confidence: .85, Score: scorer.Score{RootCauseCorrect: true}},
		{Confidence: .89, Score: scorer.Score{RootCauseCorrect: false}},
		{Confidence: 1, Score: scorer.Score{RootCauseCorrect: true}},
	}
	bins := calibrationBins(items)
	if bins[8].Count != 2 || bins[8].Correct != 1 || math.Abs(bins[8].AverageConfidence-.87) > .0001 || math.Abs(bins[8].Accuracy-.5) > .0001 {
		t.Fatalf("unexpected 0.8 bin: %+v", bins[8])
	}
	if bins[9].Count != 1 || bins[9].Correct != 1 {
		t.Fatalf("confidence=1 should be in final bin: %+v", bins[9])
	}
}

func TestAgentMetricsAreAggregated(t *testing.T) {
	items := []CaseResult{
		{CaseID: "a", Status: "passed", AgentIterations: 4, AgentToolUses: 4, AgentCorrections: 1, SafetyRejections: 2, SelfCorrectionAttempts: 1, SelfCorrectionSucceeded: true, HypothesisCount: 2, HypothesisConverged: true, EvidenceQueries: 2, EvidenceEfficiency: .5},
		{CaseID: "b", Status: "failed", AgentIterations: 2, AgentToolUses: 2, HypothesisCount: 1, EvidenceQueries: 1},
	}
	summary, err := Write(t.TempDir(), Manifest{RunID: "metrics"}, items)
	if err != nil {
		t.Fatal(err)
	}
	if summary.MeanAgentIterations != 3 || summary.TotalSafetyRejections != 2 || summary.SelfCorrectionSuccessRate != 1 || summary.HypothesisConvergenceRate != .5 || summary.MeanHypothesisCount != 1.5 || summary.MeanEvidenceEfficiency != .25 {
		t.Fatalf("unexpected metrics: %+v", summary)
	}
}

func TestWriteDirDoesNotUseRunIDAsDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "diagnosis", "standard", "20260805T143012.418Z")
	if _, err := WriteDir(dir, Manifest{RunID: "01opaque", Profile: "standard"}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "summary.json")); err != nil {
		t.Fatalf("summary not written to selected directory: %v", err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "01opaque")); !os.IsNotExist(err) {
		t.Fatalf("opaque run ID unexpectedly used as directory")
	}
}
