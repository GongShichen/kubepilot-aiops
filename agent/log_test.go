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

func TestIndexedLogSeverityFiltering(t *testing.T) {
	if templateLevel(`{"level":"INFO","path":"/metrics"}`) != "info" {
		t.Fatal("JSON log level was not detected")
	}
	if containsLogFailureMarker(`{"level":"INFO","path":"/metrics"}`) {
		t.Fatal("ordinary INFO template was treated as a failure")
	}
	if !containsLogFailureMarker(`{"level":"INFO","message":"downstream timeout"}`) {
		t.Fatal("failure-bearing INFO template should remain eligible")
	}
}
