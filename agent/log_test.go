package agent

import (
	"strings"
	"testing"
)

func TestIncidentLogQueryExcludesRetrievalDataset(t *testing.T) {
	query := incidentLogQuery("kubepilot-benchmark", "gateway-service")
	if !strings.Contains(query, `benchmark_dataset!="retrieval"`) {
		t.Fatalf("query does not isolate retrieval corpus: %s", query)
	}
}
