package reporter

import (
	"math"
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
	// CPU F1 is 2/3 and the other four categories score zero.
	if math.Abs(got-(2.0/3.0)/5.0) > .0001 {
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
	if len(metrics) != 5 {
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
