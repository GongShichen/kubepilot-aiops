package artifactlayout

import (
	"path/filepath"
	"testing"
	"time"
)

func TestRunDirectoryIsHumanReadableAndStable(t *testing.T) {
	started := time.Date(2026, 8, 5, 14, 30, 12, 418000000, time.FixedZone("CST", 8*60*60))
	want := filepath.Join("artifacts", "benchmark", "diagnosis", "standard", "20260805T063012.418Z")
	if got := RunDirectory("artifacts/benchmark", "Diagnosis", "Standard", started); got != want {
		t.Fatalf("RunDirectory=%q, want %q", got, want)
	}
	if got := RunLabel("Diagnosis", "Standard", started); got != "diagnosis-standard-20260805T063012.418Z" {
		t.Fatalf("RunLabel=%q", got)
	}
}
