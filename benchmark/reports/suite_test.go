package reports

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteSuiteSeparatesBenchmarkSections(t *testing.T) {
	dir := t.TempDir()
	if err := WriteSuite(dir, SuiteReport{LogRetrieval: map[string]any{"recall_at_5": .8}, IncidentRetrieval: map[string]any{"recall_at_5": .9}}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "benchmark_report.json"))
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err := json.Unmarshal(b, &value); err != nil {
		t.Fatal(err)
	}
	if _, ok := value["log_retrieval"]; !ok {
		t.Fatal("log retrieval section missing")
	}
	if _, ok := value["incident_retrieval"]; !ok {
		t.Fatal("incident retrieval section missing")
	}
}
