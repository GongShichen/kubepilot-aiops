package scorer

import (
	"testing"

	"github.com/kubepilot-aiops/kubepilot/benchmark/scenarios"
	"github.com/kubepilot-aiops/kubepilot/internal/domain"
)

func TestIncidentScoresOnlyCitedEvidence(t *testing.T) {
	scenario := scenarios.Scenario{Target: "payment-service", Variant: "pool_exhausted", GroundTruth: scenarios.GroundTruth{
		RootCauseCategory: "database", Service: "payment-service", Resource: "payment-service",
		RequiredEvidence: []string{"log_template", "trace"},
	}}
	incident := &domain.Incident{
		RootCauseCategory: "database", RootCauseVariant: "pool_exhausted", Service: "payment-service", Resource: "payment-service",
		RootCauseEvidenceIDs: []string{"log", "noise"},
		Evidence: []domain.Evidence{
			{ID: "log", Kind: "log_template", Source: "loki"},
			{ID: "trace", Kind: "trace", Source: "jaeger"},
			{ID: "noise", Kind: "deployment_availability", Source: "prometheus"},
		},
	}
	score := Incident(scenario, incident)
	if !score.RootCauseCorrect || !score.StrictRootCause {
		t.Fatalf("root cause score=%#v", score)
	}
	if score.EvidencePrecision != .5 || score.EvidenceRecall != .5 || score.EvidenceGroundedness != 1 {
		t.Fatalf("evidence score=%#v", score)
	}
}

func TestIncidentRejectsUncitedCollectedEvidence(t *testing.T) {
	scenario := scenarios.Scenario{Variant: "busy_loop", GroundTruth: scenarios.GroundTruth{
		RootCauseCategory: "cpu", Service: "gateway-service", Resource: "gateway-service",
		RequiredEvidence: []string{"cpu"},
	}}
	incident := &domain.Incident{
		RootCauseCategory: "cpu", RootCauseVariant: "busy_loop", Service: "gateway-service", Resource: "gateway-service",
		Evidence: []domain.Evidence{{ID: "cpu", Kind: "cpu"}},
	}
	score := Incident(scenario, incident)
	if !score.RootCauseCorrect || score.StrictRootCause || score.EvidenceRecall != 0 {
		t.Fatalf("score=%#v", score)
	}
}

func TestIncidentRequiresExactRootCauseVariant(t *testing.T) {
	scenario := scenarios.Scenario{Variant: "pool_exhausted", GroundTruth: scenarios.GroundTruth{
		RootCauseCategory: "database", Service: "payment-service", Resource: "payment-service",
	}}
	incident := &domain.Incident{
		RootCauseCategory: "database", RootCauseVariant: "invalid_credentials",
		Service: "payment-service", Resource: "payment-service",
	}
	score := Incident(scenario, incident)
	if !score.CategoryCorrect || score.VariantCorrect || score.RootCauseCorrect || score.StrictRootCause {
		t.Fatalf("variant mismatch must fail root-cause localization: %#v", score)
	}
}

func TestIncidentMapsCurrentMetricToRequiredEvidenceKind(t *testing.T) {
	scenario := scenarios.Scenario{Variant: "busy_loop", GroundTruth: scenarios.GroundTruth{
		RootCauseCategory: "cpu", Service: "gateway-service", Resource: "gateway-service", RequiredEvidence: []string{"cpu"},
	}}
	incident := &domain.Incident{
		RootCauseCategory: "cpu", RootCauseVariant: "busy_loop", Service: "gateway-service", Resource: "gateway-service",
		RootCauseEvidenceIDs: []string{"current"}, Evidence: []domain.Evidence{{ID: "current", Kind: "cpu_current"}},
	}
	score := Incident(scenario, incident)
	if score.EvidencePrecision != 1 || score.EvidenceRecall != 1 || !score.StrictRootCause {
		t.Fatalf("current metric should satisfy cpu evidence: %#v", score)
	}
}
