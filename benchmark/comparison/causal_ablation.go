package comparison

import (
	"fmt"

	"github.com/kubepilot-aiops/kubepilot/benchmark/reporter"
	"github.com/kubepilot-aiops/kubepilot/internal/domain"
)

type CausalAblationSystem struct {
	Mode               string   `json:"mode"`
	Valid              bool     `json:"valid"`
	Cases              int      `json:"cases"`
	SafetyViolations   int      `json:"safety_violations"`
	DiagnosisAccuracy  Interval `json:"diagnosis_accuracy"`
	RecoverySuccess    Interval `json:"recovery_success"`
	EvidenceEfficiency Interval `json:"evidence_efficiency"`
}

type CausalAblationReport struct {
	RunID       string                 `json:"run_id"`
	Valid       bool                   `json:"valid"`
	Systems     []CausalAblationSystem `json:"systems"`
	Comparisons []PairwiseResult       `json:"comparisons"`
}

func BuildCausalAblation(runID string, cases map[string][]reporter.CaseResult) (CausalAblationReport, error) {
	modes := []string{domain.CausalModeNone, domain.CausalModeStatic, domain.CausalModeLearned, domain.CausalModeFull}
	report := CausalAblationReport{RunID: runID, Valid: true}
	for _, mode := range modes {
		items, ok := cases[mode]
		if !ok {
			return CausalAblationReport{}, fmt.Errorf("missing causal ablation mode %s", mode)
		}
		for _, item := range items {
			if item.CausalMode != "" && item.CausalMode != mode {
				return CausalAblationReport{}, fmt.Errorf("causal mode %s artifact contains %s", mode, item.CausalMode)
			}
		}
		modelItems := withoutInfrastructureFailures(items)
		infrastructureFailures, safetyViolations := 0, 0
		for _, item := range items {
			if item.InfrastructureFailure {
				infrastructureFailures++
			}
			if item.SafetyViolation {
				safetyViolations++
			}
		}
		valid := len(items) > 0 && float64(infrastructureFailures)/float64(len(items)) <= .02 && safetyViolations == 0
		report.Valid = report.Valid && valid
		report.Systems = append(report.Systems, CausalAblationSystem{
			Mode: mode, Valid: valid, Cases: len(items), SafetyViolations: safetyViolations,
			DiagnosisAccuracy:  stratifiedInterval(modelItems, func(item reporter.CaseResult) float64 { return boolValue(item.Score.StrictRootCause) }),
			RecoverySuccess:    stratifiedInterval(modelItems, func(item reporter.CaseResult) float64 { return boolValue(item.VerificationOK) }),
			EvidenceEfficiency: stratifiedInterval(modelItems, func(item reporter.CaseResult) float64 { return item.EvidenceEfficiency }),
		})
	}
	baseline := cases[domain.CausalModeNone]
	for _, mode := range modes[1:] {
		paired, err := pairCases(baseline, cases[mode])
		if err != nil {
			return CausalAblationReport{}, fmt.Errorf("pair causal mode %s: %w", mode, err)
		}
		report.Comparisons = append(report.Comparisons,
			binaryComparison(domain.CausalModeNone, mode, "strict_diagnosis_accuracy", paired, func(item reporter.CaseResult) bool { return item.Score.StrictRootCause }),
			binaryComparison(domain.CausalModeNone, mode, "recovery_success", paired, func(item reporter.CaseResult) bool { return item.VerificationOK }),
			continuousComparison(domain.CausalModeNone, mode, "evidence_efficiency", paired, func(item reporter.CaseResult) float64 { return item.EvidenceEfficiency }),
		)
	}
	applyHolmCorrection(report.Comparisons)
	return report, nil
}
