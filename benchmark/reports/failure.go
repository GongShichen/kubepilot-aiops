// Package reports writes benchmark result artifacts. Generated reports stay
// outside the source tree under the caller-selected artifact directory.
package reports

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type FailureReport struct {
	Status    string    `json:"status"`
	Phase     string    `json:"phase"`
	Category  string    `json:"category"`
	Reason    string    `json:"reason"`
	Impact    string    `json:"impact"`
	Timestamp time.Time `json:"timestamp"`
}

func WriteFailure(path, phase, category, reason, impact string, now time.Time) error {
	if path == "" {
		return fmt.Errorf("failure report path is required")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	value := FailureReport{Status: "FAILED", Phase: phase, Category: category, Reason: reason, Impact: impact, Timestamp: now.UTC()}
	b, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o640)
}
