package suite

import "testing"

func TestLogBoundaryRejectsReasoningSignals(t *testing.T) {
	if err := ValidateSectionBoundary("log_retrieval", map[string]any{"topology_graph": true}); err == nil {
		t.Fatal("expected topology/log boundary violation")
	}
	if err := ValidateSectionBoundary("log_retrieval", map[string]any{"template_id": "t1"}); err != nil {
		t.Fatal(err)
	}
}

func TestIncidentBoundaryRejectsTemplateTruth(t *testing.T) {
	if err := ValidateSectionBoundary("incident_retrieval", map[string]any{"template_id": "t1"}); err == nil {
		t.Fatal("expected template/incident boundary violation")
	}
}
