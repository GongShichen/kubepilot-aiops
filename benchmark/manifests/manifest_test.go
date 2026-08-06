package manifests

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAutonomousManifestLoadsAndHashes(t *testing.T) {
	m, h, err := Load("../manifests/default.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if m.Version == "" || h == "" {
		t.Fatalf("invalid manifest: %+v %q", m, h)
	}
	if _, err = os.Stat(filepath.Join(".", "default.yaml")); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeManifestDoesNotContainCredentials(t *testing.T) {
	t.Setenv("CHAT_API_KEY", "must-not-appear")
	t.Setenv("CHAT_BASE_URL", "https://example.invalid")
	r := RuntimeFromEnv(Manifest{Version: "test", DatasetVersion: "dataset", RetrievalConfig: map[string]any{"k": 1}, BudgetConfig: map[string]any{"k": 1}}, "commit", time.Unix(1, 0))
	dir := t.TempDir()
	path := filepath.Join(dir, "runtime.json")
	if err := WriteRuntime(path, r); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) == "" || string(b) == "must-not-appear" {
		t.Fatalf("invalid or secret-bearing runtime manifest: %s", b)
	}
}
