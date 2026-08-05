package reports

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Envelope is the common artifact shape for every benchmark suite.
// Evaluator-only labels are intentionally absent.
type Envelope struct {
	Benchmark   string      `json:"benchmark"`
	GeneratedAt time.Time   `json:"generated_at"`
	Dataset     DatasetInfo `json:"dataset"`
	Manifest    any         `json:"manifest,omitempty"`
	Metrics     any         `json:"metrics,omitempty"`
	Cases       int         `json:"cases"`
	FailedCases []string    `json:"failed_cases,omitempty"`
	Limitations []string    `json:"limitations,omitempty"`
}

type DatasetInfo struct {
	Name           string         `json:"name"`
	Version        string         `json:"version,omitempty"`
	Size           int            `json:"size"`
	CategoryCounts map[string]int `json:"category_counts,omitempty"`
}

func WriteEnvelope(path string, envelope Envelope) error {
	if path == "" {
		return fmt.Errorf("report path is required")
	}
	if envelope.GeneratedAt.IsZero() {
		envelope.GeneratedAt = time.Now().UTC()
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	b, err := json.MarshalIndent(envelope, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o640)
}
