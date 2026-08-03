package agent

import (
	"strings"
	"testing"
)

func TestIncidentLogQueryUsesOnlyOperationalScope(t *testing.T) {
	query := incidentLogQuery("production", "gateway-service")
	if query != `{namespace="production",service="gateway-service"} |~ "(?i)(error|exception|timeout|killed|failed)"` {
		t.Fatalf("unexpected query: %s", query)
	}
	if strings.Contains(strings.ToLower(query), "benchmark") {
		t.Fatalf("runtime query contains evaluation-specific logic: %s", query)
	}
}
