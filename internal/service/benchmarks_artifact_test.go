package service

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kubepilot-aiops/kubepilot/internal/domain"
)

func TestBenchmarkStartUsesLogicalArtifactDirectory(t *testing.T) {
	manager := NewBenchmarkManager("true", "", "", "", "", t.TempDir(), NewHub())
	run, err := manager.Start("standard", false)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(filepath.Base(run.ArtifactRoot), run.ID) {
		t.Fatalf("opaque run ID leaked into artifact directory: %s", run.ArtifactRoot)
	}
	parts := strings.Split(filepath.ToSlash(run.ArtifactRoot), "/")
	if len(parts) < 3 || parts[len(parts)-3] != "diagnosis" || parts[len(parts)-2] != "standard" {
		t.Fatalf("unexpected artifact layout: %s", run.ArtifactRoot)
	}
}

func TestBenchmarkResultsReadOnlyTheRunReferencedArtifact(t *testing.T) {
	root := t.TempDir()
	exact := filepath.Join(root, "exact.json")
	decoy := filepath.Join(root, "newer.json")
	if err := os.WriteFile(exact, []byte(`{"run":"exact"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(decoy, []byte(`{"run":"decoy"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	manager := NewBenchmarkManager("missing-binary", "", "", "", "", root, NewHub())
	manager.runs["comparison"] = &BenchmarkRun{ID: "comparison", Status: "completed", ArtifactRoot: root, ResultArtifact: exact}
	value, err := manager.Results("comparison")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(value)
	if !strings.Contains(string(raw), `"exact"`) || strings.Contains(string(raw), `"decoy"`) {
		t.Fatalf("result artifact was guessed instead of referenced: %s", raw)
	}
}

func TestInterruptedComparisonCanBeQueuedForCheckpointResume(t *testing.T) {
	manager := NewBenchmarkManager(filepath.Join(t.TempDir(), "missing-binary"), "", "", "", "", t.TempDir(), NewHub())
	manager.runs["interrupted"] = &BenchmarkRun{ID: "interrupted", Profile: "standard", Status: "interrupted", Strategies: []string{domain.DiagnosisMethodDirect, domain.DiagnosisMethodKubePilot}, DatasetSplit: "test", Seeds: []int64{1}, Repetitions: 1, ArtifactRoot: t.TempDir(), CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	run, err := manager.Resume("interrupted")
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != "queued" || len(run.Strategies) != 2 {
		t.Fatalf("unexpected resumed run snapshot: %+v", run)
	}
	for attempt := 0; attempt < 100; attempt++ {
		current, getErr := manager.Get("interrupted")
		if getErr != nil {
			t.Fatal(getErr)
		}
		if current.Status == "failed" {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("resumed test process did not terminate")
}

func TestBenchmarkRunSnapshotsDoNotShareOutputStorage(t *testing.T) {
	manager := NewBenchmarkManager("true", "", "", "", "", t.TempDir(), NewHub())
	run, err := manager.Start("standard", false)
	if err != nil {
		t.Fatal(err)
	}
	manager.mu.Lock()
	manager.runs[run.ID].Output = append(manager.runs[run.ID].Output, "internal")
	manager.mu.Unlock()
	if len(run.Output) != 0 {
		t.Fatalf("returned run shares mutable output with manager: %v", run.Output)
	}
	listed := manager.List()
	if len(listed) != 1 || len(listed[0].Output) != 1 {
		t.Fatalf("unexpected list snapshot: %+v", listed)
	}
	listed[0].Output[0] = "caller mutation"
	stored, err := manager.Get(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Output[0] != "internal" {
		t.Fatalf("list snapshot mutated manager state: %v", stored.Output)
	}
}
