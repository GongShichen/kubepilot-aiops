package evidence

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/kubepilot-aiops/kubepilot/internal/domain"
)

func TestNormalizeProjectsDataAndStableScopeIntoWorkerView(t *testing.T) {
	incident := &domain.Incident{Namespace: "team-a", Service: "checkout", Resource: "checkout"}
	request := domain.EvidenceRequest{Source: "metric", Targets: []domain.ResourceRef{{Namespace: "team-a", Service: "checkout", Resource: "checkout"}}, WindowStart: time.Now().Add(-time.Minute), WindowEnd: time.Now()}
	input := []domain.Evidence{{Source: "prometheus", Kind: "cpu", Data: map[string]any{"current_value": .94, "baseline_value": .20}, Summary: "cpu observation"}}
	first := Normalize(incident, request, input)
	second := Normalize(incident, request, input)
	if first[0].ID == "" || first[0].ID != second[0].ID {
		t.Fatalf("evidence ID is not stable: %q %q", first[0].ID, second[0].ID)
	}
	view := View(first[0], 2048)
	if view.Facts["current_value"] != .94 || view.Namespace != "team-a" || view.Service != "checkout" || view.Resource != "checkout" {
		t.Fatalf("canonical worker view lost facts or scope: %+v", view)
	}
}

func TestViewsTruncateFieldsWithoutStarvingLaterEvidence(t *testing.T) {
	items := []domain.Evidence{
		{ID: "large", Source: "loki", Type: "log", Summary: "large", Facts: map[string]any{"message": strings.Repeat("故障", 10_000)}},
		{ID: "later", Source: "prometheus", Type: "cpu", Summary: "later", Facts: map[string]any{"current_value": .95}},
	}
	views := Views(items, 4096, 512, 12)
	if len(views) != 2 || views[1].ID != "later" || len(views[0].TruncatedFields) == 0 {
		t.Fatalf("large evidence starved later facts: %+v", views)
	}
	raw, err := json.Marshal(views)
	if err != nil || len(raw) > 4096 {
		t.Fatalf("bounded views are invalid: bytes=%d err=%v", len(raw), err)
	}
}
