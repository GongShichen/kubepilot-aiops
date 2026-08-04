package reports

import (
	"github.com/kubepilot-aiops/kubepilot/benchmark/manifests"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteReport(t *testing.T) {
	dir := t.TempDir()
	if err := Write(dir, Report{Manifest: manifests.Manifest{Version: "test"}, DatasetSize: 1}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"report.json", "report.md"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
}
