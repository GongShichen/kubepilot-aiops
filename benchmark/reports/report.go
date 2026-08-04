// Package reports writes benchmark result artifacts. It is deliberately a
// library only; production runs never import it and generated reports stay
// outside the source tree under the caller-selected artifact directory.
package reports

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/kubepilot-aiops/kubepilot/benchmark/manifests"
)

type Report struct {
	Manifest    manifests.Manifest `json:"manifest"`
	GeneratedAt time.Time          `json:"generated_at"`
	DatasetSize int                `json:"dataset_size"`
	Components  map[string]any     `json:"components,omitempty"`
	Reasoning   map[string]any     `json:"reasoning,omitempty"`
	Agent       map[string]any     `json:"agent,omitempty"`
	Incident    map[string]any     `json:"incident,omitempty"`
	Evolution   map[string]any     `json:"evolution,omitempty"`
	Limitations []string           `json:"limitations,omitempty"`
}

type FailureReport struct {
	Status    string    `json:"status"`
	Phase     string    `json:"phase"`
	Category  string    `json:"category"`
	Reason    string    `json:"reason"`
	Impact    string    `json:"impact"`
	Timestamp time.Time `json:"timestamp"`
}

func Write(dir string, report Report) error {
	if dir == "" {
		return fmt.Errorf("report directory is required")
	}
	if report.GeneratedAt.IsZero() {
		report.GeneratedAt = time.Now().UTC()
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	b, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	if err = os.WriteFile(filepath.Join(dir, "report.json"), b, 0o640); err != nil {
		return err
	}
	md := fmt.Sprintf("# KubePilot Autonomous Incident Benchmark\n\n- Dataset size: %d\n- Generated at: %s\n\nAll scores are produced by the evaluator side of the benchmark. Ground truth is not part of Agent input.\n", report.DatasetSize, report.GeneratedAt.Format(time.RFC3339))
	return os.WriteFile(filepath.Join(dir, "report.md"), []byte(md), 0o640)
}

func WriteFailure(path, phase, category, reason, impact string, now time.Time) error {
	if path == "" { return fmt.Errorf("failure report path is required") }
	if now.IsZero() { now = time.Now().UTC() }
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil { return err }
	value := FailureReport{Status: "FAILED", Phase: phase, Category: category, Reason: reason, Impact: impact, Timestamp: now.UTC()}
	b, err := json.MarshalIndent(value, "", "  "); if err != nil { return err }
	return os.WriteFile(path, b, 0o640)
}
