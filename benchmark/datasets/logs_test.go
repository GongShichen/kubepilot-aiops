package datasets

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFaultTemplatesCoverAllCategories(t *testing.T) {
	items := FaultTemplates()
	if len(items) != 100 {
		t.Fatalf("templates=%d, want 100", len(items))
	}
	ids := map[string]bool{}
	categories := map[string]int{}
	for _, item := range items {
		if ids[item.ID] {
			t.Fatalf("duplicate ID %s", item.ID)
		}
		ids[item.ID] = true
		categories[item.Category]++
		if strings.Contains(item.Symptom, item.ID) {
			t.Fatalf("symptom leaks ID %s", item.ID)
		}
	}
	for _, category := range []string{"cpu", "memory", "database", "network", "deployment"} {
		if categories[category] != 20 {
			t.Fatalf("%s templates=%d, want 20", category, categories[category])
		}
	}
}

func TestGenerateLogsIncludesTargetsNoiseAndDynamicFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "logs.jsonl")
	if err := GenerateLogs(path, 10_000, 20260803); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	counts := map[string]int{}
	namespaces := map[string]bool{}
	targetCombinations := map[string]bool{}
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var record LogRecord
		if err = json.Unmarshal(scanner.Bytes(), &record); err != nil {
			t.Fatal(err)
		}
		counts[record.RecordType]++
		namespaces[record.Namespace] = true
		if record.RecordType == "target_fault" {
			targetCombinations[record.TemplateID+"/"+record.Namespace+"/"+record.Service] = true
		}
		if record.RequestID == "" || record.OrderID == "" || record.ClientIP == "" || record.TraceID == "" {
			t.Fatalf("missing dynamic fields: %#v", record)
		}
	}
	if err = scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if counts["normal"] < 7_700 || counts["target_fault"] < 1_400 || counts["interference"] < 300 {
		t.Fatalf("unexpected distribution: %#v", counts)
	}
	if len(namespaces) != 3 {
		t.Fatalf("namespaces=%v", namespaces)
	}
	if len(targetCombinations) != 900 {
		t.Fatalf("target combinations=%d, want 900", len(targetCombinations))
	}
}
