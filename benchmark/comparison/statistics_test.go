package comparison

import (
	"math"
	"testing"
	"time"

	"github.com/kubepilot-aiops/kubepilot/benchmark/reporter"
	"github.com/kubepilot-aiops/kubepilot/benchmark/scorer"
	"github.com/kubepilot-aiops/kubepilot/internal/domain"
)

func TestBuildProducesPairedStatistics(t *testing.T) {
	strategies := []string{domain.DiagnosisMethodRuleOnly, domain.DiagnosisMethodEvidence, domain.DiagnosisMethodCognitive, domain.DiagnosisMethodActive, domain.DiagnosisMethodReAct}
	cases := map[string][]reporter.CaseResult{}
	summaries := map[string]reporter.Summary{}
	for _, strategy := range strategies {
		correct := strategy == domain.DiagnosisMethodActive
		item := reporter.CaseResult{CaseID: "memory-leak", IncidentID: "incident", Seed: 7, Repetition: 1, Category: "memory", RootCauseVariant: "container-leak", Service: "payment", Resource: "payment-pod", Score: scorer.Score{StrictRootCause: correct}, VerificationOK: correct, Duration: time.Second, EstimatedModelCost: .01}
		switch strategy {
		case domain.DiagnosisMethodReAct:
			item.Architecture = "single-react"
		case domain.DiagnosisMethodRuleOnly:
			item.Architecture, item.PlannerTasks, item.WorkerFindings = "eino-rule-diagnosis-runtime", 4, 4
		case domain.DiagnosisMethodEvidence:
			item.Architecture, item.PlannerTasks, item.WorkerFindings = "eino-evidence-diagnosis-runtime", 4, 4
		case domain.DiagnosisMethodCognitive, domain.DiagnosisMethodActive:
			item.Architecture, item.PlannerTasks, item.WorkerFindings = domain.WorkflowRuntimeName, 4, 4
		}
		cases[strategy] = []reporter.CaseResult{item}
		summaries[strategy] = reporter.Summary{Total: 1, Valid: true}
	}
	report, err := Build("run", "standard", summaries, cases)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Systems) != 5 || len(report.Comparisons) != 16 {
		t.Fatalf("unexpected report sizes: %+v", report)
	}
	if report.Comparisons[0].Difference.Estimate != 1 || report.Comparisons[0].Pairs != 1 {
		t.Fatalf("unexpected paired comparison: %+v", report.Comparisons[0])
	}
	if len(report.Systems[0].Breakdowns) != 4 || report.Systems[0].Breakdowns[0].Dimension != "category" {
		t.Fatalf("missing deterministic report breakdowns: %+v", report.Systems[0].Breakdowns)
	}
}

func TestMcNemarAndWilcoxonAreBounded(t *testing.T) {
	if p := mcnemarExact(0, 5); p <= 0 || p > 1 {
		t.Fatalf("invalid McNemar p=%f", p)
	}
	p, effect := wilcoxonSignedRank([]float64{1, 2, 3, -1})
	if p <= 0 || p > 1 || math.Abs(effect) > 1 {
		t.Fatalf("invalid Wilcoxon result p=%f effect=%f", p, effect)
	}
}
