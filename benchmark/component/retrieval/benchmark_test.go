package retrieval

import "testing"

func TestEvaluateComponentStrategies(t *testing.T) {
	q := []Query{{ID: "q1", Relevant: map[string]float64{"shared-db": 1}}}
	r := Evaluate("hybrid", q, []Result{{QueryID: "q1", RankedIDs: []string{"shared-db", "noise"}}})
	if r.RecallAt1 != 1 || r.MRR != 1 {
		t.Fatalf("unexpected component score: %+v", r)
	}
}

func TestRetrievalStrategiesAreExplicit(t *testing.T) {
	for _, strategy := range []string{"semantic", "lexical", "topology", "hybrid", "reranker"} {
		if err := ValidateStrategy(strategy); err != nil {
			t.Fatal(err)
		}
	}
	if ValidateStrategy("unknown") == nil {
		t.Fatal("unknown strategy accepted")
	}
}
