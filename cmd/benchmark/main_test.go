package main

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/kubepilot-aiops/kubepilot/benchmark/reporter"
)

func TestRetrievalCollectionNameIsDimensionAndModelSpecific(t *testing.T) {
	first := retrievalCollectionName("model-a", 1024, "run-a")
	if first == retrievalCollectionName("model-b", 1024, "run-a") {
		t.Fatal("different models must use different collections")
	}
	if first == retrievalCollectionName("model-a", 128, "run-a") {
		t.Fatal("different dimensions must use different collections")
	}
	if first == retrievalCollectionName("model-a", 1024, "run-b") {
		t.Fatal("different runs must use different collections")
	}
	if !strings.HasPrefix(first, "kubepilot_benchmark_logs_1024_") {
		t.Fatalf("unexpected collection name %q", first)
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

func TestManifestContainsReproducibilityDimensions(t *testing.T) {
	raw, err := os.ReadFile("../../benchmark/manifests/default.yaml")
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, key := range []string{"code_commit:", "model:", "embedding_model:", "reranker:", "skill_hash:", "budget_config:", "retrieval_weights:", "ranking_weights:"} {
		if !strings.Contains(text, key) {
			t.Fatalf("manifest missing %s", key)
		}
	}
}
