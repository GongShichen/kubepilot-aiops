package comparison

import (
	"testing"

	"github.com/kubepilot-aiops/kubepilot/benchmark/reporter"
	"github.com/kubepilot-aiops/kubepilot/benchmark/scorer"
	"github.com/kubepilot-aiops/kubepilot/internal/domain"
)

func TestCausalAblationUsesPairedLiveOutcomes(t *testing.T) {
	cases := map[string][]reporter.CaseResult{}
	for _, mode := range []string{domain.CausalModeNone, domain.CausalModeStatic, domain.CausalModeLearned, domain.CausalModeFull} {
		for index := 0; index < 6; index++ {
			correct := mode == domain.CausalModeFull || index < 2
			cases[mode] = append(cases[mode], reporter.CaseResult{CaseID: string(rune('a' + index)), Seed: 1, Repetition: 1, Category: "memory", CausalMode: mode, Score: scorer.Score{StrictRootCause: correct}, VerificationOK: correct, EvidenceEfficiency: float64(index+1) / 10})
		}
	}
	report, err := BuildCausalAblation("causal-run", cases)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Valid || len(report.Systems) != 4 || len(report.Comparisons) != 9 || report.Systems[3].DiagnosisAccuracy.Estimate != 1 {
		t.Fatalf("unexpected causal ablation report: %+v", report)
	}
}
