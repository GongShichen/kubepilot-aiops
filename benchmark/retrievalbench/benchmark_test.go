package retrievalbench

import (
	"strings"
	"testing"
	"time"

	"github.com/kubepilot-aiops/kubepilot/benchmark/datasets"
	"github.com/kubepilot-aiops/kubepilot/reasoning"
	"github.com/kubepilot-aiops/kubepilot/retrieval"
	"github.com/kubepilot-aiops/kubepilot/tools"
)

func TestGenerateQueriesCoversEveryFaultTemplateWithoutIDLeakage(t *testing.T) {
	queries := generateQueries()
	if len(queries) != 500 {
		t.Fatalf("queries=%d, want 500", len(queries))
	}
	templateCounts := map[string]int{}
	categoryCounts := map[string]int{}
	texts := map[string]bool{}
	for _, query := range queries {
		templateCounts[query.ExpectedTemplate]++
		categoryCounts[query.Category]++
		if strings.Contains(strings.ToLower(query.Text), strings.ToLower(query.ExpectedTemplate)) {
			t.Fatalf("query leaks template ID %s: %s", query.ExpectedTemplate, query.Text)
		}
		if texts[query.Text] {
			t.Fatalf("duplicate query text: %s", query.Text)
		}
		texts[query.Text] = true
	}
	if len(templateCounts) != 100 {
		t.Fatalf("templates=%d, want 100", len(templateCounts))
	}
	for templateID, count := range templateCounts {
		if count != 5 {
			t.Fatalf("template %s queries=%d, want 5", templateID, count)
		}
	}
	for _, category := range []string{"cpu", "memory", "database", "network", "deployment"} {
		if categoryCounts[category] != 100 {
			t.Fatalf("category %s queries=%d, want 100", category, categoryCounts[category])
		}
	}
}

func TestLexicalRankUsesContentNotGroundTruthOrder(t *testing.T) {
	entries := []tools.LokiEntry{
		{Line: "database session authentication failed", Labels: map[string]string{"template_id": "database-01"}},
		{Line: "worker goroutine consumed processor capacity", Labels: map[string]string{"template_id": "cpu-01"}},
	}
	ranked, candidates := lexicalRank("pod consumed all processor capacity", entries)
	if candidates != 2 || len(ranked) != 2 || ranked[0] != "cpu-01" {
		t.Fatalf("ranked=%v candidates=%d", ranked, candidates)
	}
}

func TestRankDocsRequiresTemplateServiceAndNamespace(t *testing.T) {
	query := Query{ExpectedTemplate: "cpu-01", Service: "gateway-service", Namespace: "kubepilot-benchmark"}
	docs := []retrieval.Document{
		{Template: "cpu-01", Service: "order-service", Namespace: "kubepilot-benchmark"},
		{Template: "cpu-01", Service: "gateway-service", Namespace: "kubepilot-benchmark"},
	}
	if got := rankDocs(query, docs); got != 2 {
		t.Fatalf("rank=%d, want 2", got)
	}
}

func TestClusterQualityUsesActualMappings(t *testing.T) {
	clusters := map[int]map[string]int{
		1: {"cpu-01": 8, "cpu-02": 2},
		2: {"database-01": 5},
	}
	count, purity := clusterQuality(clusters)
	if count != 2 || purity < .866 || purity > .867 {
		t.Fatalf("count=%d purity=%f", count, purity)
	}
}

func TestSummarizeCandidateReductionAndLatency(t *testing.T) {
	metrics := summarize([]Result{{Strategy: "hybrid", Rank: 1, Latency: 2 * time.Millisecond, BackendLatency: time.Millisecond, CandidateCount: 10, CorpusCount: 100, FusionLatency: 500 * time.Microsecond}, {Strategy: "hybrid", Rank: 20, Latency: 3 * time.Millisecond, BackendLatency: 2 * time.Millisecond, CandidateCount: 10, CorpusCount: 100}})
	if len(metrics) != 1 || metrics[0].CandidateReduction != .9 || metrics[0].Recall1 != .5 || metrics[0].BackendP50MS != 2 || metrics[0].MRR != .5 || metrics[0].NDCG <= .5 {
		t.Fatalf("metrics=%#v", metrics)
	}
	if metrics[0].StageLatency["fusion"].P50MS != .5 {
		t.Fatalf("stage metrics=%#v", metrics[0].StageLatency)
	}
}

func TestLokiQueryWindowUsesIngestionTimeNotCurrentWallClock(t *testing.T) {
	first := time.Date(2026, 8, 3, 1, 0, 0, 0, time.UTC)
	last := first.Add(10 * time.Second)
	start, end := lokiQueryWindow(first, last)
	if !start.Equal(first.Add(-time.Minute)) || !end.Equal(last.Add(time.Minute)) {
		t.Fatalf("window=%s..%s", start, end)
	}
	if end.Sub(start) != 2*time.Minute+10*time.Second {
		t.Fatalf("window duration=%s", end.Sub(start))
	}
}

func TestTopologyCandidatesAllowSharedDependencyAcrossServices(t *testing.T) {
	docs := []retrieval.Document{{ID: "payment", Namespace: "n", Service: "payment-service"}, {ID: "gateway", Namespace: "n", Service: "gateway-service"}}
	records := map[string]datasets.LogRecord{
		"payment": {Namespace: "n", Service: "payment-service", Message: "mysql connection refused"},
		"gateway": {Namespace: "n", Service: "gateway-service", Message: "request completed"},
	}
	items := topologyCandidates(Query{Namespace: "n", Service: "order-service", Text: "downstream mysql timeout"}, docs, records, 50, nil, reasoning.New(reasoning.DefaultConfig()))
	found := false
	for _, item := range items {
		found = found || item.Service == "payment-service"
	}
	if !found {
		t.Fatalf("shared MySQL dependency did not recall cross-service history: %#v", items)
	}
}
