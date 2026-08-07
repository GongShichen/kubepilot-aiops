package store

import (
	"testing"
	"time"

	"github.com/kubepilot-aiops/kubepilot/internal/domain"
)

func TestHypothesisRecordIDIsIncidentScoped(t *testing.T) {
	if hypothesisRecordID("incident-a", "h1") == hypothesisRecordID("incident-b", "h1") {
		t.Fatal("hypothesis database keys must be globally unique across Incidents")
	}
}

func TestEvidenceRecordIDIsIncidentScoped(t *testing.T) {
	if evidenceRecordID("incident-a", "kubernetes-stable-observation", 0) == evidenceRecordID("incident-b", "kubernetes-stable-observation", 0) {
		t.Fatal("evidence database keys must be globally unique across Incidents")
	}
	if got := evidenceRecordID("incident-a", "", 3); got != "incident-a-evidence-3" {
		t.Fatalf("fallback evidence key=%q", got)
	}
}

func TestDiagnosticIntelligencePayloadIncludesModelUsage(t *testing.T) {
	usage := domain.ModelUsageEvent{IncidentID: "incident-a", Agent: "cognitive_runtime", Phase: "diagnosis", InputTokens: 12, OutputTokens: 34, CreatedAt: time.Now().UTC()}
	payload := diagnosticIntelligencePayload(&domain.Investigation{ModelUsage: []domain.ModelUsageEvent{usage}})
	items, ok := payload["model_usage"].([]domain.ModelUsageEvent)
	if !ok || len(items) != 1 || items[0].Agent != usage.Agent || items[0].OutputTokens != usage.OutputTokens {
		t.Fatalf("model usage was omitted from investigation audit payload: %#v", payload)
	}
}
