package evidence

import (
	"testing"
	"time"

	"github.com/kubepilot-aiops/kubepilot/internal/domain"
)

func testPolicy() Policy {
	return Policy{
		Evidence: map[string]float64{"neural_similarity": .30, "temporal_alignment": .20, "service_resource_attribution": .20, "trace_request_pod_attribution": .15, "causal_contribution": .10, "source_quality_and_rarity": .05},
		Incident: map[string]float64{"neural_similarity": .40, "topology_similarity": .20, "normalized_rrf": .15, "evidence_feature_overlap": .10, "causal_path_coverage": .05, "service_resource_proximity": .05, "revision_temporal_context": .05},
		Topology: map[string]float64{"directed_edge_jaccard": .40, "shared_critical_dependency": .30, "root_to_symptom_path_similarity": .20, "failing_node_role_similarity": .10},
	}
}

func TestEvidenceRankingUsesAttributionAndDeterministicFallback(t *testing.T) {
	now := time.Now().UTC()
	incident := &domain.Incident{Namespace: "ns", Service: "payment", Resource: "payment", CreatedAt: now}
	items := []domain.Evidence{
		{ID: "unattributed", Source: "loki", Namespace: "ns", Service: "other", Resource: "other", Timestamp: now.Add(-4 * time.Minute), WindowStart: now.Add(-5 * time.Minute), WindowEnd: now, NeuralScore: 1, NeuralRanked: false},
		{ID: "attributed", Source: "kubernetes", Namespace: "ns", Service: "payment", Resource: "payment", Timestamp: now, WindowStart: now.Add(-5 * time.Minute), WindowEnd: now},
	}
	ranked := Rank(testPolicy(), incident, items)
	if ranked[0].ID != "attributed" || ranked[0].Attribution == nil || ranked[0].Attribution.AttributionScore <= ranked[1].Attribution.AttributionScore {
		t.Fatalf("attribution did not dominate an unavailable neural score: %+v", ranked)
	}
	if ranked[1].NeuralRanked || ranked[1].RelevanceScore >= 1 {
		t.Fatal("deterministic fallback fabricated a neural result")
	}
}

func TestIncidentRankingUsesIndependentPolicy(t *testing.T) {
	items := []domain.RetrievalCandidate{
		{IncidentID: "semantic", Rank: domain.RankBreakdown{NeuralRanked: true, NeuralSimilarity: 1}},
		{IncidentID: "topology", Rank: domain.RankBreakdown{NeuralRanked: true, TopologySimilarity: 1, NormalizedRRF: 1, EvidenceFeatureOverlap: 1, CausalPathCoverage: 1, ServiceResourceProximity: 1, RevisionTemporalContext: 1}},
	}
	ranked := RankCandidates(testPolicy(), items)
	if ranked[0].IncidentID != "semantic" {
		t.Fatalf("incident policy was not applied independently: %+v", ranked)
	}
	if ranked[0].Rank.IncidentRank == nil || ranked[0].Rank.IncidentRank.DeterministicWeight != .45 || ranked[0].Rank.IncidentRank.NeuralWeight != .55 {
		t.Fatalf("incident rank breakdown did not record the required fusion weights: %+v", ranked[0].Rank.IncidentRank)
	}
}

func TestRerankerFusionUsesSeparateEvidenceAndIncidentWeights(t *testing.T) {
	now := time.Now().UTC()
	incident := &domain.Incident{Namespace: "ns", Service: "payment", Resource: "payment", CreatedAt: now}
	evidence := Rank(testPolicy(), incident, []domain.Evidence{{ID: "e1", Source: "kubernetes", Namespace: "ns", Service: "payment", Resource: "payment", Timestamp: now, WindowStart: now.Add(-time.Minute), WindowEnd: now, NeuralScore: .8, NeuralRanked: true}})
	if len(evidence) != 1 || evidence[0].RankBreakdown == nil {
		t.Fatalf("missing evidence rank breakdown: %+v", evidence)
	}
	if evidence[0].RankBreakdown.EvidenceID != "e1" || evidence[0].RankBreakdown.Factors["service_resource_attribution"] == 0 {
		t.Fatalf("evidence ranking did not expose final factors: %+v", evidence[0].RankBreakdown)
	}
	if got, want := evidence[0].RankBreakdown.FinalScore, .70*evidence[0].RankBreakdown.DeterministicScore+.30*.8; got < want-1e-9 || got > want+1e-9 {
		t.Fatalf("evidence fusion mismatch: got %.6f want %.6f", got, want)
	}
	candidates := RankCandidates(testPolicy(), []domain.RetrievalCandidate{{IncidentID: "i1", Rank: domain.RankBreakdown{DeterministicScore: .4, NeuralRanked: true, NeuralSimilarity: .9}}})
	if len(candidates) != 1 || candidates[0].Rank.IncidentRank == nil {
		t.Fatalf("missing incident rank breakdown: %+v", candidates)
	}
	if candidates[0].Rank.IncidentRank.IncidentID != "i1" || candidates[0].Rank.IncidentRank.Factors["deterministic_score"] == 0 {
		t.Fatalf("incident ranking did not expose final factors: %+v", candidates[0].Rank.IncidentRank)
	}
	if got, want := candidates[0].Rank.FinalScore, .45*.4+.55*.9; got < want-1e-9 || got > want+1e-9 {
		t.Fatalf("incident fusion mismatch: got %.6f want %.6f", got, want)
	}
}

func TestNeuralScoreChangesFinalEvidenceAndIncidentOrdering(t *testing.T) {
	now := time.Now().UTC()
	incident := &domain.Incident{Namespace: "ns", Service: "payment", Resource: "payment", CreatedAt: now}
	evidence := []domain.Evidence{
		{ID: "deterministic", Source: "kubernetes", Namespace: "ns", Service: "payment", Resource: "payment", Timestamp: now, WindowStart: now.Add(-time.Minute), WindowEnd: now, NeuralScore: .05, NeuralRanked: true},
		{ID: "neural", Source: "loki", Namespace: "ns", Service: "other", Resource: "other", Timestamp: now, WindowStart: now.Add(-5 * time.Minute), WindowEnd: now, NeuralScore: 1, NeuralRanked: true},
	}
	rankedEvidence := Rank(testPolicy(), incident, evidence)
	if rankedEvidence[0].ID != "neural" {
		t.Fatalf("neural relevance did not affect final evidence ordering: %+v", rankedEvidence)
	}

	candidates := RankCandidates(testPolicy(), []domain.RetrievalCandidate{
		{IncidentID: "deterministic", Rank: domain.RankBreakdown{DeterministicScore: .9, NeuralRanked: true, NeuralSimilarity: .05}},
		{IncidentID: "neural", Rank: domain.RankBreakdown{DeterministicScore: .2, NeuralRanked: true, NeuralSimilarity: .95}},
	})
	if candidates[0].IncidentID != "neural" {
		t.Fatalf("neural relevance did not affect final incident ordering: %+v", candidates)
	}
}
