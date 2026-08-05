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
