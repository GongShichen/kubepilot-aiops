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

func TestDiagnosticIntelligencePayloadIncludesAssistantTurnAudit(t *testing.T) {
	observedAt := time.Now().UTC()
	turn := domain.AssistantTurnRecord{TurnID: "turn-1", ToolCallPresent: true, ReasoningPresent: true, Persisted: true, ObservedAt: observedAt}
	payload := diagnosticIntelligencePayload(&domain.Investigation{AssistantTurns: []domain.AssistantTurnRecord{turn}})
	items, ok := payload["assistant_turns"].([]domain.AssistantTurnRecord)
	if !ok || len(items) != 1 || items[0].TurnID != turn.TurnID || !items[0].ToolCallPresent || !items[0].ReasoningPresent || !items[0].Persisted {
		t.Fatalf("Assistant turn audit was omitted from investigation payload: %#v", payload)
	}
}

func TestKubePilotWorkflowArchitectureNeverUsesLegacyRuntimeBeforeInvestigation(t *testing.T) {
	for _, method := range []string{
		domain.DiagnosisMethodKubePilot,
		domain.DiagnosisMethodKubePilotNoReflection,
		domain.DiagnosisMethodKubePilotNoOptionalSkills,
	} {
		incident := &domain.Incident{DiagnosisMethod: method}
		if got := incidentWorkflowArchitecture(incident); got != "eino-native-self-reflective-brain" {
			t.Fatalf("method %q initial architecture=%q", method, got)
		}
	}
}

func TestWorkflowArchitectureUsesPersistedInvestigationProjection(t *testing.T) {
	incident := &domain.Incident{
		DiagnosisMethod: domain.DiagnosisMethodDirect,
		Investigation:   &domain.Investigation{Architecture: "single-pass"},
	}
	if got := incidentWorkflowArchitecture(incident); got != "single-pass" {
		t.Fatalf("persisted architecture=%q", got)
	}
}
