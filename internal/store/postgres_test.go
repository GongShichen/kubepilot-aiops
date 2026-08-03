package store

import "testing"

func TestHypothesisRecordIDIsIncidentScoped(t *testing.T) {
	if hypothesisRecordID("incident-a", "h1") == hypothesisRecordID("incident-b", "h1") {
		t.Fatal("hypothesis database keys must be globally unique across Incidents")
	}
}
