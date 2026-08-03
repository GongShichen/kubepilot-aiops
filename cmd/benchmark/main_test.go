package main

import (
	"strings"
	"testing"
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
