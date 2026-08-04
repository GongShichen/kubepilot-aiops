package incident

import (
	"fmt"
	"github.com/kubepilot-aiops/kubepilot/benchmark/evaluator"
	"github.com/kubepilot-aiops/kubepilot/benchmark/scenarios"
)

type Dataset struct {
	Version string `json:"version"`
	Cases   []Case `json:"cases"`
	Hash    string `json:"hash"`
}

// LoadCatalog adapts the existing immutable scenario catalog into the
// evaluator-side incident dataset. Ground truth remains in Case.Expected and
// is never returned by AgentInput.
func LoadCatalog(path string) (Dataset, error) {
	catalog, items, hash, err := scenarios.Load(path)
	if err != nil {
		return Dataset{}, err
	}
	out := Dataset{Version: catalog.Version, Cases: make([]Case, 0, len(items)), Hash: hash}
	for _, s := range items {
		out.Cases = append(out.Cases, Case{ID: s.ID, Category: s.Category, Description: s.Description, Input: Input{Namespace: s.Namespace, Service: s.Service, Resource: s.Target, Severity: "warning", Summary: s.Description, Alerts: []Alert{{Name: s.Category + "_anomaly", Severity: "warning"}}}, Expected: ExpectedFromScenario(s), Fault: FaultSpec{Kind: s.Injector, Target: s.Target, Parameters: s.InjectParams}, Timeout: TimeoutSpec{Diagnosis: s.Timeouts.Diagnosis, Recovery: s.Timeouts.Recovery}})
	}
	return out, nil
}
func ExpectedFromScenario(s scenarios.Scenario) evaluator.Expected {
	return evaluator.Expected{RootCause: s.GroundTruth.RootCauseDetail, Category: s.GroundTruth.RootCauseCategory, Service: s.GroundTruth.Service, Resource: s.GroundTruth.Resource, EvidenceIDs: append([]string(nil), s.GroundTruth.RequiredEvidence...), RecoveryAction: first(s.GroundTruth.AllowedRecoveryActions), RecoveryTarget: s.Target, CausalPath: nil}
}
func first(v []string) string {
	if len(v) > 0 {
		return v[0]
	}
	return ""
}
func (d Dataset) Validate() error {
	if len(d.Cases) == 0 {
		return fmt.Errorf("incident dataset is empty")
	}
	seen := map[string]bool{}
	for _, c := range d.Cases {
		if c.ID == "" || seen[c.ID] {
			return fmt.Errorf("invalid or duplicate case %q", c.ID)
		}
		seen[c.ID] = true
		if c.Expected.RootCause == "" || c.Expected.Category == "" {
			return fmt.Errorf("case %q is missing evaluator ground truth", c.ID)
		}
	}
	return nil
}
