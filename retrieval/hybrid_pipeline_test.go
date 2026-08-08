package retrieval

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/kubepilot-aiops/kubepilot/internal/domain"
)

func TestHybridRetrieveGeneratesFromAllOperationalChannels(t *testing.T) {
	now := time.Now().UTC()
	engine := IncidentRetrievalEngine{HistoricalRetriever: HistoricalRetriever{Embedder: engineEmbedder{}, Vectors: engineVectors{}, Knowledge: engineKnowledge{}}}
	features := domain.IncidentFeatures{IncidentID: "current", Cluster: "cluster-a", Namespace: "kubepilot-demo", Service: "payment-service", Terms: []string{"database", "timeout"}, WindowStart: now.Add(-5 * time.Minute), WindowEnd: now, TopologyServices: []string{"mysql"}, Observed: map[string]float64{"latency": .9}}
	result, err := engine.Retrieve(context.Background(), domain.HybridRetrievalQuery{IncidentID: "current", Features: features, Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Channels) != 5 || len(result.Final) == 0 || result.SnapshotHash == "" || result.Query.Intent == "" || len(result.FusionProfile.ChannelWeights) != 5 {
		t.Fatalf("hybrid retrieval is incomplete: %+v", result)
	}
	wanted := map[domain.RetrievalChannel]bool{domain.RetrievalBM25: true, domain.RetrievalVector: true, domain.RetrievalGraph: true, domain.RetrievalTemporal: true, domain.RetrievalMetric: true}
	generated := map[domain.RetrievalChannel]bool{}
	for _, channel := range result.Channels {
		delete(wanted, channel.Channel)
		generated[channel.Channel] = channel.Available && len(channel.Candidates) > 0
	}
	if len(wanted) != 0 {
		t.Fatalf("missing retrieval channels: %+v", wanted)
	}
	for channel, ok := range generated {
		if !ok {
			t.Fatalf("retrieval channel %s did not independently generate candidates: %+v", channel, result.Channels)
		}
	}
	seen := map[string]bool{}
	for _, candidate := range result.Fused {
		seen[candidate.IncidentID] = true
	}
	if !seen["temporal-only"] || !seen["metric-only"] {
		t.Fatalf("temporal or metric-only candidate was lost before fusion: %+v", result.Fused)
	}
}

func TestFuseHybridChannelsAdmitsGraphOnlyCandidate(t *testing.T) {
	graphOnly := domain.RetrievalCandidate{IncidentID: "graph-only", SourceScores: map[string]float64{"topology": .9}}
	items := fuseHybridChannels(map[domain.RetrievalChannel][]domain.RetrievalCandidate{domain.RetrievalGraph: {graphOnly}}, hybridFusionProfile(domain.HybridQueryUnderstanding{}).ChannelWeights, 10)
	if len(items) != 1 || items[0].IncidentID != "graph-only" || items[0].Rank.TopologyScore == 0 {
		t.Fatalf("graph channel did not generate a candidate: %+v", items)
	}
}

func TestFuseHybridChannelsPreservesTopFiftyCandidatePool(t *testing.T) {
	lexical := make([]domain.RetrievalCandidate, 0, 60)
	vector := make([]domain.RetrievalCandidate, 0, 60)
	for index := 0; index < 60; index++ {
		candidate := domain.RetrievalCandidate{IncidentID: fmt.Sprintf("incident-%02d", index)}
		lexical = append(lexical, candidate)
		vector = append(vector, candidate)
	}
	items := fuseHybridChannels(map[domain.RetrievalChannel][]domain.RetrievalCandidate{domain.RetrievalBM25: lexical, domain.RetrievalVector: vector}, hybridFusionProfile(domain.HybridQueryUnderstanding{}).ChannelWeights, 50)
	if len(items) != 50 {
		t.Fatalf("hybrid fusion cannot support Recall@50: got %d candidates", len(items))
	}
}

func TestHybridQueryUnderstandingAdaptsFusionWithoutSelectingRootCause(t *testing.T) {
	understanding := domain.HybridQueryUnderstanding{Intent: "explain latency spike after deployment through an upstream dependency", Entities: []string{"payment", "database"}, Signals: []string{"latency", "increase"}}
	profile := hybridFusionProfile(understanding)
	if profile.ChannelWeights[domain.RetrievalGraph] <= .9 || profile.ChannelWeights[domain.RetrievalTemporal] <= .7 || profile.ChannelWeights[domain.RetrievalMetric] <= .9 {
		t.Fatalf("incident intent did not adapt independent retrieval channels: %+v", profile)
	}
	if len(profile.Reasons) < 4 {
		t.Fatalf("fusion profile lacks replayable reasons: %+v", profile)
	}
}

func TestWorldModelEnrichesHybridQueryWithoutModelGeneratedFacts(t *testing.T) {
	now := time.Now().UTC()
	model := &domain.OperationalWorldModel{
		Cluster: "cluster-a", Namespace: "team-a", EvidenceSnapshotHash: "evidence-snapshot",
		Entities:         []domain.OperationalEntity{{ID: "service/team-a/payment", Kind: "service", Service: "payment", State: "degraded"}, {ID: "database/team-a/mysql", Kind: "database", Service: "mysql"}},
		Relations:        []domain.OperationalRelation{{From: "service/team-a/payment", To: "database/team-a/mysql", Kind: "calls"}},
		AbnormalSignals:  []domain.OperationalSignal{{ID: "signal-1", Category: "dependency", Signal: "connection_timeout", Direction: "increase", EvidenceID: "e1"}},
		MetricSignatures: []domain.MetricSignature{{Name: "latency", Value: .9, EvidenceID: "e2"}},
		Timeline:         []domain.OperationalEvent{{ID: "e1", OccurredAt: now.Add(-5 * time.Minute)}, {ID: "e2", OccurredAt: now}},
	}
	features := enrichFeaturesFromWorldModel(domain.IncidentFeatures{Service: "payment"}, model)
	if features.Cluster != "cluster-a" || features.Namespace != "team-a" || len(features.Observed) != 1 || features.Observed["latency"] != .9 || !hasTemporalFeatures(features) || len(features.TopologyGraph.Nodes) != 2 || len(features.TopologyGraph.Edges) != 1 {
		t.Fatalf("World Model did not become an operational retrieval query: %+v", features)
	}
	foundSignal := false
	for _, term := range features.Terms {
		foundSignal = foundSignal || term == "connection_timeout"
	}
	if !foundSignal {
		t.Fatalf("server-owned abnormal signal was absent from hybrid query terms: %+v", features.Terms)
	}
}

func TestHybridRetrieveUsesExternalRerankerAsFinalStage(t *testing.T) {
	now := time.Now().UTC()
	engine := IncidentRetrievalEngine{HistoricalRetriever: HistoricalRetriever{Embedder: engineEmbedder{}, Vectors: engineVectors{}, Knowledge: engineKnowledge{}}, Reranker: pipelineReranker{}}
	result, err := engine.Retrieve(context.Background(), domain.HybridRetrievalQuery{IncidentID: "current", Features: domain.IncidentFeatures{Cluster: "cluster-a", Namespace: "kubepilot-demo", Service: "payment-service", Terms: []string{"database", "timeout"}, WindowStart: now.Add(-5 * time.Minute), WindowEnd: now, Observed: map[string]float64{"latency": .9}}, Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if !result.RerankerUsed || len(result.Final) == 0 || !result.Final[0].Rank.NeuralRanked {
		t.Fatalf("external reranker was not the final hybrid stage: %+v", result)
	}
}
