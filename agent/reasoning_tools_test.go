package agent

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/kubepilot-aiops/kubepilot/internal/domain"
)

func TestDiagnosisCandidateContextIsCompactAndExplainable(t *testing.T) {
	items := []domain.RetrievalCandidate{{
		IncidentID: "historical-1",
		Summary:    "shared database dependency failed",
		Rank:       domain.RankBreakdown{FinalScore: .88},
		Features: domain.IncidentFeatures{
			Terms:            []string{"large", "internal", "feature", "payload"},
			TopologyServices: []string{"mysql"},
			CausalNodeIDs:    []string{"database_unavailable"},
		},
	}}
	payload, err := json.Marshal(diagnosisCandidateContext(items))
	if err != nil {
		t.Fatal(err)
	}
	text := string(payload)
	if strings.Contains(text, `"features"`) || strings.Contains(text, "large") {
		t.Fatalf("internal feature payload was retained: %s", text)
	}
	if !strings.Contains(text, `"final_score":0.88`) || !strings.Contains(text, `"topology_services":["mysql"]`) {
		t.Fatalf("rank explanation was lost: %s", text)
	}
}

func TestDiagnosisEvidenceAndPatternContextAreCompact(t *testing.T) {
	evidence := diagnosisEvidenceContext([]domain.Evidence{{ID: "e1", Source: "prometheus", Type: "cpu", Summary: "cpu observation", Content: map[string]any{"result": 1}, Data: map[string]any{"duplicate": true}, RankingReasons: []string{"causal"}}})
	patterns := diagnosisPatternContext([]domain.CausalPattern{{ID: "p1", Category: "cpu", Cause: "cpu saturation", Nodes: []domain.CausalNode{{ID: "cpu_pressure", Match: []string{"large internal matcher payload"}}}, Edges: []domain.CausalEdge{{From: "cpu_pressure", To: "latency"}}, Source: "builtin", Status: "active"}})
	payload, err := json.Marshal(map[string]any{"evidence": evidence, "patterns": patterns})
	if err != nil {
		t.Fatal(err)
	}
	text := string(payload)
	for _, forbidden := range []string{"duplicate", "large internal matcher payload", `"status"`, `"source":"builtin"`} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("internal context %q was retained: %s", forbidden, text)
		}
	}
	if !strings.Contains(text, `"node_ids":["cpu_pressure"]`) || !strings.Contains(text, `"result":1`) {
		t.Fatalf("diagnostic facts were lost: %s", text)
	}
}

func TestRootCauseRankerUsesDeterministicGates(t *testing.T) {
	evidence := []domain.Evidence{{ID: "e1", Source: "kubernetes"}, {ID: "e2", Source: "loki"}}
	weak := domain.VerifiedHypothesis{Draft: domain.HypothesisDraft{ID: "weak"}, Status: domain.HypothesisSupported, FinalScore: .79, VerifiedEvidenceIDs: []string{"e1", "e2"}}
	strong := domain.VerifiedHypothesis{Draft: domain.HypothesisDraft{ID: "strong"}, Status: domain.HypothesisSupported, FinalScore: .91, VerifiedEvidenceIDs: []string{"e1", "e2"}}
	result := rankRootCause(rootRankInput{Verified: []domain.VerifiedHypothesis{weak, strong}, Evidence: evidence})
	if result.NeedsAttention || result.Selected == nil || result.Selected.Draft.ID != "strong" {
		t.Fatalf("unexpected deterministic selection: %#v", result)
	}
	strong.MissingCausalNodes = []string{"trace_gap"}
	result = rankRootCause(rootRankInput{Verified: []domain.VerifiedHypothesis{strong}, Evidence: evidence})
	if !result.RequestAdditionalEvidence || result.NeedsAttention {
		t.Fatalf("missing causal node did not request one bounded collection: %#v", result)
	}
	strong.MissingCausalNodes = nil
	strong.VerifiedEvidenceIDs = []string{"e1"}
	result = rankRootCause(rootRankInput{Verified: []domain.VerifiedHypothesis{strong}, Evidence: evidence})
	if !result.NeedsAttention {
		t.Fatalf("single-source evidence passed root-cause gate: %#v", result)
	}
}

func TestArbitrationReturnsAuditableGateFailuresWithoutLoweringThresholds(t *testing.T) {
	evidence := []domain.Evidence{{ID: "kube", Source: "kubernetes"}, {ID: "metric", Source: "prometheus"}}
	weak := domain.VerifiedHypothesis{Draft: domain.HypothesisDraft{ID: "weak"}, Status: domain.HypothesisEvidenceSearching, SupportingScore: .64, CausalPathCoverage: .5, MissingCausalNodes: []string{"missing"}, FinalScore: .79, ContradictionScore: .11, VerifiedEvidenceIDs: []string{"kube", "metric"}}
	result := arbitrateHypotheses([]domain.VerifiedHypothesis{weak}, evidence)
	if result.Accepted || len(result.GateResults) != 1 {
		t.Fatalf("weak hypothesis passed or gate audit missing: %+v", result)
	}
	failed := result.GateResults[0].FailedGates
	for _, gate := range []string{"supported_status", "supporting_score", "causal_coverage", "final_score", "contradiction"} {
		if !slices.Contains(failed, gate) {
			t.Fatalf("gate %q missing from audit: %+v", gate, result)
		}
	}
}

func TestArbitrationAuditsEmptyCandidateUniverse(t *testing.T) {
	result := arbitrateHypotheses(nil, nil)
	if result.Accepted || len(result.GateResults) != 1 || !slices.Contains(result.GateResults[0].FailedGates, "no_candidate") {
		t.Fatalf("empty candidate universe was not recorded as a safe audit outcome: %+v", result)
	}
}

func TestToolEvidenceObservationIsUTF8SafeAndBounded(t *testing.T) {
	items := make([]domain.Evidence, 20)
	for index := range items {
		items[index] = domain.Evidence{ID: string(rune('a' + index)), Source: "loki", Summary: strings.Repeat("故障", 1000), Content: map[string]any{"message": strings.Repeat("错误", 5000)}}
	}
	bounded := compactToolEvidence(items, 8<<10)
	payload, err := json.Marshal(bounded)
	if err != nil {
		t.Fatal(err)
	}
	if len(payload) > 8<<10 || len(bounded) == 0 || !json.Valid(payload) {
		t.Fatalf("bounded tool observation is invalid: bytes=%d items=%d", len(payload), len(bounded))
	}
	for _, item := range bounded {
		if len(item.Facts) == 0 {
			t.Fatalf("bounded model evidence lost its canonical facts: %+v", item)
		}
	}
}
