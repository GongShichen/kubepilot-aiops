package incident_retrieval

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestAgentContextExcludesTruth(t *testing.T) {
	incident := Incident{IncidentID: "i1", Service: "payment", Namespace: "demo", RootCause: "memory_leak", RelatedIncidents: []string{"i2"}}
	b, err := json.Marshal(incident.AgentContext())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "memory_leak") || strings.Contains(string(b), "i2") {
		t.Fatalf("evaluator data leaked into agent context: %s", b)
	}
}

func TestIncidentRankingMetricsUseRelatedIncidents(t *testing.T) {
	dataset := Dataset{
		Version: "incident-retrieval",
		Incidents: []Incident{{
			IncidentID: "q", Service: "payment", Namespace: "demo", RootCause: "x", RelatedIncidents: []string{"h"},
		}},
	}
	report := Evaluate(dataset, []Observation{{QueryID: "q", Strategy: StrategyFull, RankedIDs: []string{"h"}}})
	if len(report.Strategies) != 1 || report.Strategies[0].RecallAt1 != 1 {
		t.Fatalf("unexpected incident retrieval metrics: %+v", report)
	}
}

func TestLoadExpandedBalancesStructuredQueries(t *testing.T) {
	dataset, err := LoadExpanded("../../benchmark/datasets/incidents/structured.yaml", 500)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(dataset.Incidents); got != 500 {
		t.Fatalf("incident query count = %d, want 500", got)
	}
	counts := dataset.CategoryCounts()
	for _, category := range []string{"memory", "database", "network", "deployment", "cpu"} {
		if counts[category] != 100 {
			t.Fatalf("category %s count = %d, want 100", category, counts[category])
		}
	}
}
