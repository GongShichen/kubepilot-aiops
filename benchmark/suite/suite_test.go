package suite

import (
	"path/filepath"
	"testing"

	"github.com/kubepilot-aiops/kubepilot/benchmark/evolution"
	incidentretrieval "github.com/kubepilot-aiops/kubepilot/benchmark/incident_retrieval"
)

func TestManifestAndComponents(t *testing.T) {
	_, _, err := ValidateManifest(filepath.Join("..", "manifests", "autonomous.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(ComponentNames()) != 6 {
		t.Fatalf("unexpected component count: %d", len(ComponentNames()))
	}
}

func TestDatasetsAreCapabilitySeparated(t *testing.T) {
	if _, err := incidentretrieval.Load(filepath.Join("..", "datasets", "incidents", "structured.yaml")); err != nil {
		t.Fatal(err)
	}
	if _, err := evolution.LoadResolved(filepath.Join("..", "datasets", "resolved_incidents", "resolved.yaml")); err != nil {
		t.Fatal(err)
	}
}
