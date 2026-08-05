package correlation

import (
	"testing"
)

func TestGenerateCorrelationDataset(t *testing.T) {
	items := Generate(100, 2, 8, 20260803)
	expected := Expected(items)
	if len(items) < 200 || len(expected) != len(items) {
		t.Fatalf("unexpected generated dataset: alerts=%d expected=%d", len(items), len(expected))
	}
	groups := map[string]bool{}
	for _, group := range expected {
		groups[group] = true
	}
	if len(groups) != 100 {
		t.Fatalf("groups=%d, want 100", len(groups))
	}
}
