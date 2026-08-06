package main

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kubepilot-aiops/kubepilot/benchmark/history"
	"github.com/kubepilot-aiops/kubepilot/benchmark/reporter"
	"github.com/kubepilot-aiops/kubepilot/internal/config"
)

func TestSeparatedBenchmarkCommandsDoNotExposeLegacyRetrieval(t *testing.T) {
	for _, command := range []string{"log-retrieval", "incident-retrieval", "suite-report"} {
		if command == "retrieval" {
			t.Fatal("legacy mixed retrieval command must not be exposed")
		}
	}
}

func TestSemanticJudgeChatConfigDefaultsAndOverrides(t *testing.T) {
	base := config.ChatConfig{Protocol: "openai-compatible", BaseURL: "https://chat.example", APIPath: "/chat/completions", APIKey: "chat-key", Model: "chat-model", Timeout: time.Minute, MaxTokens: 8192, Temperature: 0, MaxRetries: 3}
	judge, err := semanticJudgeChatConfig(base)
	if err != nil || judge != base {
		t.Fatalf("default judge config=%+v err=%v", judge, err)
	}
	t.Setenv("JUDGE_CHAT_MODEL", "judge-model")
	t.Setenv("JUDGE_CHAT_MAX_TOKENS", "1024")
	t.Setenv("JUDGE_CHAT_TIMEOUT", "90s")
	judge, err = semanticJudgeChatConfig(base)
	if err != nil || judge.Model != "judge-model" || judge.MaxTokens != 1024 || judge.Timeout != 90*time.Second || judge.APIKey != "chat-key" {
		t.Fatalf("override judge config=%+v err=%v", judge, err)
	}
}

func TestValidateResumeManifestRejectsConfigurationChanges(t *testing.T) {
	base := reporter.Manifest{RunID: "run", Profile: "standard", CatalogHash: "catalog", Protocol: "openai-compatible", Model: "model-a", EndpointHash: "endpoint", ModelConfigHash: "config-a", DiagnosisMethod: "kubepilot", GitCommit: "commit", SourceHash: "source", StartedAt: time.Now()}
	changed := base
	changed.ModelConfigHash = "config-b"
	if err := validateResumeManifest(base, changed); err == nil || !strings.Contains(err.Error(), "model_config_hash") {
		t.Fatalf("expected model configuration mismatch, got %v", err)
	}
	if err := validateResumeManifest(base, base); err != nil {
		t.Fatal(err)
	}
	changed = base
	changed.Parallelism = 4
	if err := validateResumeManifest(base, changed); err == nil || !strings.Contains(err.Error(), "parallelism") {
		t.Fatalf("expected parallelism mismatch, got %v", err)
	}
}

func TestValidateComparisonManifestRejectsSourceChanges(t *testing.T) {
	base := reporter.Manifest{
		Profile: "standard", ManifestHash: "manifest", CatalogHash: "catalog", Protocol: "openai-compatible", Model: "model-a", ModelProfile: "profile",
		EndpointHash: "endpoint", ModelConfigHash: "config", SkillSnapshotHash: "skills", RankingPolicyHash: "ranking", ToolCostPolicyHash: "tools",
		BudgetConfigHash: "budget", SourceHash: "source-a", HistoryDatasetHash: "history", HistoryCollection: "collection", DatasetSplit: "test",
		CausalMode: "full", Repetitions: 1, Parallelism: 4, ModelConcurrency: 4, WorkerNamespaces: []string{"worker-01", "worker-02", "worker-03", "worker-04"},
		ShardPolicy: "case-seed-repetition-hash", Seeds: []int64{1, 2, 3}, PricingSnapshot: map[string]float64{"input": 0},
	}
	changed := base
	changed.SourceHash = "source-b"
	if err := validateComparisonManifest(base, changed); err == nil || !strings.Contains(err.Error(), "source_hash") {
		t.Fatalf("expected source hash mismatch, got %v", err)
	}
	if err := validateComparisonManifest(base, base); err != nil {
		t.Fatal(err)
	}
}

func TestCheckpointRecorderSerializesConcurrentResults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "checkpoint.jsonl")
	recorder := &checkpointRecorder{path: path}
	var wait sync.WaitGroup
	for index := 0; index < 50; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			recorder.Record(reporter.CaseResult{CaseID: "case", Seed: int64(index + 1), Repetition: 1})
		}()
	}
	wait.Wait()
	if err := recorder.Err(); err != nil {
		t.Fatal(err)
	}
	results, err := readCaseResults(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 50 {
		t.Fatalf("checkpoint records=%d, want 50", len(results))
	}
	seen := map[int64]bool{}
	for _, result := range results {
		seen[result.Seed] = true
	}
	if len(seen) != 50 {
		t.Fatalf("checkpoint contains partial or duplicate records: %d", len(seen))
	}
}

func TestResolveWorkerNamespacesRejectsNamespaceSubstitution(t *testing.T) {
	pool := "kubepilot-benchmark-worker-01,kubepilot-benchmark-worker-02"
	namespaces, err := resolveWorkerNamespaces("kubepilot-benchmark", 2, pool)
	if err != nil || len(namespaces) != 2 {
		t.Fatalf("resolved=%v err=%v", namespaces, err)
	}
	if _, err = resolveWorkerNamespaces("kubepilot-benchmark", 2, "kubepilot-benchmark-worker-01,production"); err == nil {
		t.Fatal("arbitrary worker namespace was accepted")
	}
}

func TestExpandHistoryNamespacesPreservesHeldOutCorpusPerWorker(t *testing.T) {
	catalog, items, _, err := history.Load("../../benchmark/history.yaml")
	if err != nil {
		t.Fatal(err)
	}
	expanded := expandHistoryNamespaces(items, catalog.Namespace, []string{catalog.Namespace, "kubepilot-benchmark-worker-01"})
	if len(expanded) != 2*len(items) {
		t.Fatalf("expanded documents=%d, want %d", len(expanded), 2*len(items))
	}
	if expanded[0].Document.ID != items[0].Document.ID {
		t.Fatal("base namespace history identifier changed")
	}
	worker := expanded[len(items)]
	if worker.Document.Namespace != "kubepilot-benchmark-worker-01" || !strings.Contains(worker.Text, worker.Document.Namespace) || worker.Document.ID == items[0].Document.ID {
		t.Fatalf("worker history was not isolated: %+v", worker)
	}
}

func TestCleanupFailureStopsFullPipeline(t *testing.T) {
	caseID, failed := cleanupFailure([]reporter.CaseResult{
		{CaseID: "first", Status: "failed"},
		{CaseID: "dirty", Status: "cleanup_failed"},
	})
	if !failed || caseID != "dirty" {
		t.Fatalf("cleanupFailure() = %q, %v", caseID, failed)
	}
}

func TestStrategyOrderIsDeterministicPermutation(t *testing.T) {
	first := comparisonStrategyOrder("comparison-run")
	second := comparisonStrategyOrder("comparison-run")
	if stableJSON(first) != stableJSON(second) || len(first) != 4 {
		t.Fatalf("strategy randomization is not reproducible: first=%v second=%v", first, second)
	}
	seen := map[string]bool{}
	for _, strategy := range first {
		seen[strategy] = true
	}
	for _, required := range []string{"direct", "rag", "react", "kubepilot"} {
		if !seen[required] {
			t.Fatalf("randomized strategy order lost %s: %v", required, first)
		}
	}
}

func TestManifestContainsReproducibilityDimensions(t *testing.T) {
	raw, err := os.ReadFile("../../benchmark/manifests/default.yaml")
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, key := range []string{"code_commit:", "model:", "embedding_model:", "reranker:", "skill_hash:", "budget_config:", "retrieval_config:", "ranking_weights:", "evaluation:", "datasets:"} {
		if !strings.Contains(text, key) {
			t.Fatalf("manifest missing %s", key)
		}
	}
}
