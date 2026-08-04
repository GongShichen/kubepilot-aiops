// Package isolation provides small checks used by the benchmark's contract
// tests. It has no access to production Agent or knowledge-store packages.
package isolation

import (
	"encoding/json"
	"strings"

	"github.com/kubepilot-aiops/kubepilot/benchmark/incident"
)

func AgentPayload(in incident.Input) ([]byte, error) { return json.Marshal(in) }
func ContainsEvaluatorLabel(payload []byte) bool {
	s := strings.ToLower(string(payload))
	for _, label := range []string{"expected_root_cause", "ground_truth", "expected_evidence", "allowed_recovery_actions", "scenario_id"} {
		if strings.Contains(s, label) {
			return true
		}
	}
	return false
}
func AssertAgentPayload(in incident.Input) error {
	payload, err := AgentPayload(in)
	if err != nil {
		return err
	}
	if ContainsEvaluatorLabel(payload) {
		return &IsolationError{Reason: "evaluator label in agent payload"}
	}
	return nil
}

type IsolationError struct{ Reason string }

func (e *IsolationError) Error() string { return e.Reason }
