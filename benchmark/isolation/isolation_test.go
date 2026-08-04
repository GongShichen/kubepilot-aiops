package isolation

import (
	"encoding/json"
	"testing"

	"github.com/kubepilot-aiops/kubepilot/benchmark/evaluator"
	"github.com/kubepilot-aiops/kubepilot/benchmark/incident"
)

func TestGroundTruthDoesNotEnterAgentPayload(t *testing.T) {
	if err := AssertAgentPayload(incident.Input{Namespace: "kubepilot-demo", Summary: "memory anomaly"}); err != nil {
		t.Fatal(err)
	}
}
func TestEvaluatorLabelsAreDetected(t *testing.T) {
	if !ContainsEvaluatorLabel([]byte(`{"expected_root_cause":"memory_leak"}`)) {
		t.Fatal("label was not detected")
	}
}

func TestCaseSerializationKeepsExpectedEvaluatorSide(t *testing.T) {
	b, err := json.Marshal(incident.Case{ID: "c", Input: incident.Input{Summary: "observation"}, Expected: evaluator.Expected{RootCause: "secret-label"}})
	if err != nil {
		t.Fatal(err)
	}
	if ContainsEvaluatorLabel(b) || string(b) == "" {
		t.Fatalf("evaluator data leaked into case payload: %s", b)
	}
}
