package evolution

import (
	"path/filepath"
	"testing"
)

func TestResolvedDatasetIsNotLogDataset(t *testing.T) {
	dataset, err := LoadResolved(filepath.Join("..", "datasets", "resolved_incidents", "resolved.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(dataset.Incidents) < 3 || dataset.Incidents[0].Status != "RESOLVED" {
		t.Fatalf("unexpected resolved dataset: %+v", dataset)
	}
}
