package reports

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/kubepilot-aiops/kubepilot/benchmark/manifests"
)

// SuiteReport is the suite-level report. Each section is evaluated against its
// own dataset and metric family; no log-template result is reused as an
// incident, diagnosis or knowledge-evolution score.
type SuiteReport struct {
	Manifest           manifests.Manifest `json:"manifest"`
	GeneratedAt        time.Time          `json:"generated_at"`
	LogRetrieval       any                `json:"log_retrieval,omitempty"`
	IncidentRetrieval  any                `json:"incident_retrieval,omitempty"`
	Diagnosis          any                `json:"diagnosis,omitempty"`
	Recovery           any                `json:"recovery,omitempty"`
	AgentBehavior      any                `json:"agent_behavior,omitempty"`
	KnowledgeEvolution any                `json:"knowledge_evolution,omitempty"`
	Correlation        any                `json:"correlation,omitempty"`
	Autonomous         any                `json:"autonomous_sre,omitempty"`
	Ablation           any                `json:"ablation,omitempty"`
	Limitations        []string           `json:"remaining_limitations,omitempty"`
}

func WriteSuite(dir string, report SuiteReport) error {
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
	if err = os.WriteFile(filepath.Join(dir, "benchmark_report.json"), b, 0o640); err != nil {
		return err
	}
	markdown := "# KubePilot Autonomous SRE Benchmark Report\n\n" +
		"Log Retrieval, Incident Retrieval, Diagnosis, Recovery, Agent Behavior and Knowledge Evolution are evaluated as independent suites. Ground truth is evaluator-only.\n"
	return os.WriteFile(filepath.Join(dir, "benchmark_report.md"), []byte(markdown), 0o640)
}
