package main

import (
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
