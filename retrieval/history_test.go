package retrieval

import (
	"strings"
	"testing"

	"github.com/kubepilot-aiops/kubepilot/internal/domain"
)

func TestHistoricalQueryUsesSymptomsNotGenericMetricNames(t *testing.T) {
	incident := &domain.Incident{Summary: "service degradation", Service: "order-service", Resource: "order-service", Evidence: []domain.Evidence{
		{Kind: "cpu", Summary: "Prometheus cpu query result"},
		{Kind: "memory_trend", Data: map[string]any{"result": []any{map[string]any{"values": []any{[]any{1.0, "100"}, []any{2.0, "180"}}}}}},
		{Kind: "log_template", Summary: "allocation failed under memory pressure"},
	}}
	query := strings.Join(historicalQueryParts(incident), "\n")
	if strings.Contains(query, "Prometheus cpu query result") {
		t.Fatalf("generic metric summary polluted retrieval query: %s", query)
	}
	if !strings.Contains(query, "memory working set increased monotonically") || !strings.Contains(query, "allocation failed") {
		t.Fatalf("symptom-bearing evidence missing from query: %s", query)
	}
}
