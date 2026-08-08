package domain

import (
	"encoding/json"
	"testing"
)

func TestDiagnosisMethodsAreCanonicalBeforePersistence(t *testing.T) {
	cases := map[string]string{"": DiagnosisMethodKubePilot, DiagnosisMethodDirect: DiagnosisMethodDirect, DiagnosisMethodRAG: DiagnosisMethodRAG, DiagnosisMethodReAct: DiagnosisMethodReAct}
	for input, expected := range cases {
		actual, ok := NormalizeDiagnosisMethod(input)
		if !ok || actual != expected {
			t.Fatalf("NormalizeDiagnosisMethod(%q)=(%q,%t), want %q", input, actual, ok, expected)
		}
	}
	if _, ok := NormalizeDiagnosisMethod("invented"); ok {
		t.Fatal("unknown diagnosis strategy was accepted")
	}
	for _, removed := range []string{"llm-only", "vector-rag"} {
		if _, ok := NormalizeDiagnosisMethod(removed); ok {
			t.Fatalf("removed compatibility method %q was accepted", removed)
		}
	}
}

func TestKubePilotHasDedicatedWorkflowRuntimeIdentity(t *testing.T) {
	if SupervisorRuntimeName == WorkflowRuntimeName || SupervisorRuntimeName == BrainWorkflowRuntimeName {
		t.Fatalf("Supervisor graph retained a strategy runtime identity: %q", SupervisorRuntimeName)
	}
	for _, method := range []string{"", DiagnosisMethodKubePilot, DiagnosisMethodKubePilotNoReflection, DiagnosisMethodKubePilotNoOptionalSkills} {
		if got := RuntimeNameForDiagnosisMethod(method); got != BrainWorkflowRuntimeName || got == WorkflowRuntimeName {
			t.Fatalf("KubePilot method %q retained legacy runtime identity %q", method, got)
		}
	}
	if got := RuntimeNameForDiagnosisMethod(DiagnosisMethodActive); got != WorkflowRuntimeName {
		t.Fatalf("baseline runtime identity changed: %q", got)
	}
}

func TestNormalizeCausalMode(t *testing.T) {
	for _, mode := range []string{CausalModeNone, CausalModeStatic, CausalModeLearned, CausalModeFull} {
		if normalized, ok := NormalizeCausalMode(mode); !ok || normalized != mode {
			t.Fatalf("causal mode %q normalized to %q, valid=%t", mode, normalized, ok)
		}
	}
	if normalized, ok := NormalizeCausalMode(""); !ok || normalized != CausalModeFull {
		t.Fatalf("default causal mode=%q valid=%t", normalized, ok)
	}
	if _, ok := NormalizeCausalMode("unsupported"); ok {
		t.Fatal("unsupported causal mode was accepted")
	}
}

func TestCausalEdgeReadsLegacySourceTargetButWritesCanonicalFields(t *testing.T) {
	var edge CausalEdge
	if err := json.Unmarshal([]byte(`{"source":"cause","target":"symptom","relation":"causes","confidence":0.9}`), &edge); err != nil {
		t.Fatal(err)
	}
	if edge.From != "cause" || edge.To != "symptom" {
		t.Fatalf("legacy causal edge was not migrated: %+v", edge)
	}
	raw, err := json.Marshal(edge)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err = json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	if wire["from"] != "cause" || wire["to"] != "symptom" || wire["source"] != nil || wire["target"] != nil {
		t.Fatalf("causal edge did not serialize canonically: %s", raw)
	}
}

func TestStateTransitions(t *testing.T) {
	if !CanTransition(StatusReceived, StatusCorrelating) {
		t.Fatal("expected transition")
	}
	if CanTransition(StatusReceived, StatusResolved) {
		t.Fatal("unexpected transition")
	}
	if !CanTransition(StatusProposing, StatusDiagnosing) {
		t.Fatal("pre-mutation dry-run rejection must allow a clean recovery replan")
	}
	if CanTransition(StatusRecovering, StatusDiagnosing) {
		t.Fatal("post-mutation recovery must never re-enter diagnosis automatically")
	}
}
