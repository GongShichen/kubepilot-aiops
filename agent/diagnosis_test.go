package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/kubepilot-aiops/kubepilot/internal/domain"
	llm "github.com/kubepilot-aiops/kubepilot/internal/model"
)

type scriptedDiagnosisModel struct {
	responses []llm.Response
	calls     int
}

func (m *scriptedDiagnosisModel) Complete(context.Context, []llm.Message, []llm.Tool) (llm.Response, error) {
	response := m.responses[m.calls]
	m.calls++
	return response, nil
}
func (m *scriptedDiagnosisModel) Probe(context.Context) error { return nil }
func (m *scriptedDiagnosisModel) Protocol() string            { return "openai-compatible" }

func TestCompactEvidenceBoundsModelPayload(t *testing.T) {
	items := make([]domain.Evidence, 0, 40)
	for i := 0; i < 20; i++ {
		items = append(items, domain.Evidence{ID: "trace", Source: "jaeger", Kind: "trace", Data: map[string]any{"payload": strings.Repeat("x", 4096)}})
		items = append(items, domain.Evidence{ID: "log", Source: "loki", Kind: "log_template", Data: map[string]any{"payload": strings.Repeat("x", 4096)}})
	}
	got := compactEvidence(items)
	if len(got) != 8 {
		t.Fatalf("expected three traces and five log templates, got %d", len(got))
	}
	for _, evidence := range got {
		preview, ok := evidence.Data.(map[string]any)
		if !ok || preview["truncated"] != true {
			t.Fatalf("evidence was not truncated: %#v", evidence.Data)
		}
	}
}

func TestDiagnosisRepairsUnknownEvidenceCitationOnce(t *testing.T) {
	arguments := func(evidenceID string) json.RawMessage {
		return json.RawMessage(`{"root_cause":"CPU busy loop","category":"cpu","variant":"busy_loop","service":"gateway-service","resource":"gateway-service","confidence":0.8,"evidence_ids":["` + evidenceID + `"],"hypotheses":[{"id":"h1","cause":"CPU busy loop","probability":0.8,"supporting_evidence":["` + evidenceID + `"],"falsification_conditions":["CPU usage returns to baseline"]}]}`)
	}
	model := &scriptedDiagnosisModel{responses: []llm.Response{
		{ToolCalls: []llm.ToolCall{{Name: "submit_diagnosis", Arguments: arguments("truncated-id")}}},
		{ToolCalls: []llm.ToolCall{{Name: "submit_diagnosis", Arguments: arguments("evidence-1")}}},
	}}
	incident := &domain.Incident{ID: "incident-1", Service: "gateway-service", Resource: "gateway-service", Evidence: []domain.Evidence{{ID: "evidence-1", Source: "prometheus", Kind: "cpu_current"}}}
	if err := (DiagnosisAgent{Model: model}).Run(context.Background(), incident); err != nil {
		t.Fatal(err)
	}
	if model.calls != 2 || len(incident.RootCauseEvidenceIDs) != 1 || incident.RootCauseEvidenceIDs[0] != "evidence-1" {
		t.Fatalf("calls=%d evidence=%v", model.calls, incident.RootCauseEvidenceIDs)
	}
}
