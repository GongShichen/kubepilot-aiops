package service

import (
	"path/filepath"
	"strings"
	"testing"
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
