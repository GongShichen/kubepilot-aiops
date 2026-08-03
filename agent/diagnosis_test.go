package agent

import (
	"strings"
	"testing"

	"github.com/kubepilot-aiops/kubepilot/internal/domain"
)

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
