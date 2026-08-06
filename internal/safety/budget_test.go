package safety

import (
	"errors"
	"strings"
	"testing"

	"github.com/kubepilot-aiops/kubepilot/internal/domain"
)

func TestParallelToolReservationIsAtomic(t *testing.T) {
	state := &domain.AgentBudgetState{}
	limits := map[string]domain.AgentBudget{"diagnosis": {MaxIterations: 4, MaxToolUses: 2, MaxTokens: 100, MaxCorrections: 2}}
	controller := NewBudgetController(state, limits, map[string]int{"metric": 1, "semantic": 3})
	if _, err := controller.ReserveTool("diagnosis", "metric"); err != nil {
		t.Fatal(err)
	}
	before := controller.State()
	if _, err := controller.ReserveTools("diagnosis", []string{"metric", "semantic"}); err == nil {
		t.Fatal("expected the complete parallel batch to be rejected")
	} else {
		var exhausted ErrBudgetExceeded
		if !errors.As(err, &exhausted) {
			t.Fatalf("expected ErrBudgetExceeded, got %T", err)
		}
	}
	after := controller.State()
	if before.IncidentUses != after.IncidentUses || before.IncidentCost != after.IncidentCost || before.Usage["diagnosis"] != after.Usage["diagnosis"] {
		t.Fatalf("rejected batch partially consumed budget: before=%+v after=%+v", before, after)
	}
}

func TestBudgetStateSurvivesControllerRecreation(t *testing.T) {
	state := &domain.AgentBudgetState{}
	limits := map[string]domain.AgentBudget{"diagnosis": {MaxIterations: 4, MaxToolUses: 5, MaxTokens: 100, MaxCorrections: 2}}
	first := NewBudgetController(state, limits, map[string]int{"lookup": 2})
	_, _ = first.ReserveTool("diagnosis", "lookup")
	_ = first.AddTokens("diagnosis", 25)
	remaining, err := first.UseCorrection("diagnosis")
	if err != nil || remaining != 1 {
		t.Fatalf("unexpected correction result: remaining=%d err=%v", remaining, err)
	}

	second := NewBudgetController(state, limits, map[string]int{"lookup": 2})
	snapshot := second.State()
	usage := snapshot.Usage["diagnosis"]
	if usage.ToolUses != 1 || usage.ToolCost != 2 || usage.Tokens != 25 || usage.Corrections != 1 {
		t.Fatalf("resume reset budget usage: %+v", usage)
	}
}

func TestToolBudgetIsScopedPerAgent(t *testing.T) {
	state := &domain.AgentBudgetState{}
	limits := map[string]domain.AgentBudget{
		"supervisor": {MaxToolUses: 2, MaxTokens: 100},
		"diagnosis":  {MaxToolUses: 2, MaxTokens: 100},
	}
	controller := NewBudgetController(state, limits, map[string]int{"lookup": 1})
	if _, err := controller.ReserveTools("supervisor", []string{"lookup", "lookup"}); err != nil {
		t.Fatalf("supervisor reservation incorrectly used an incident-wide tool cap: %v", err)
	}
	if remaining := controller.RemainingTools("diagnosis"); remaining != 2 {
		t.Fatalf("diagnosis budget was reduced by supervisor calls: remaining=%d", remaining)
	}
	if _, err := controller.ReserveTools("diagnosis", []string{"lookup", "lookup"}); err != nil {
		t.Fatalf("diagnosis reservation incorrectly used an incident-wide tool cap: %v", err)
	}
}

func TestToolCostIsTelemetryOnly(t *testing.T) {
	state := &domain.AgentBudgetState{}
	limits := map[string]domain.AgentBudget{"diagnosis": {MaxToolUses: 3, MaxTokens: 100}}
	controller := NewBudgetController(state, limits, map[string]int{"expensive": 1000})
	if _, err := controller.ReserveTools("diagnosis", []string{"expensive", "expensive", "expensive"}); err != nil {
		t.Fatalf("tool cost incorrectly blocked calls within the per-Agent use limit: %v", err)
	}
	usage := controller.State().Usage["diagnosis"]
	if usage.ToolUses != 3 || usage.ToolCost != 3000 {
		t.Fatalf("tool telemetry mismatch: %+v", usage)
	}
}

func TestTokenBudgetIsScopedPerAgent(t *testing.T) {
	state := &domain.AgentBudgetState{}
	limits := map[string]domain.AgentBudget{
		"supervisor": {MaxToolUses: 1, MaxTokens: 100},
		"diagnosis":  {MaxToolUses: 1, MaxTokens: 100},
	}
	controller := NewBudgetController(state, limits, nil)
	if err := controller.AddTokens("supervisor", 100); err != nil {
		t.Fatal(err)
	}
	if err := controller.AddTokens("diagnosis", 100); err != nil {
		t.Fatalf("supervisor tokens incorrectly reduced the diagnosis budget: %v", err)
	}
	if got := controller.State().IncidentTokens; got != 200 {
		t.Fatalf("aggregate token telemetry=%d, want 200", got)
	}
}

func TestIterationCorrectionAndTokenAccountingBoundaries(t *testing.T) {
	controller := NewBudgetController(nil, map[string]domain.AgentBudget{"diagnosis": {MaxIterations: 1, MaxToolUses: 1, MaxTokens: 10, MaxCorrections: 1}}, map[string]int{"inspect": 2})
	if err := controller.AddIteration("diagnosis"); err != nil {
		t.Fatal(err)
	}
	if err := controller.AddIteration("diagnosis"); err == nil || !strings.Contains(err.Error(), "iterations") {
		t.Fatalf("iteration limit error=%v", err)
	}
	if err := controller.AddTokens("diagnosis", 10); err != nil {
		t.Fatal(err)
	}
	if err := controller.AddTokens("diagnosis", 1); err != nil {
		t.Fatalf("cumulative Agent output incorrectly triggered a total cap: %v", err)
	}
	if err := controller.AddTokens("diagnosis", 11); err != nil {
		t.Fatalf("provider-reported output must remain telemetry after dispatch: %v", err)
	}
	if usage := controller.State().Usage["diagnosis"]; usage.Tokens != 22 {
		t.Fatalf("cumulative output telemetry mismatch after enforcement: %+v", usage)
	}
	if _, err := controller.UseCorrection("diagnosis"); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.UseCorrection("diagnosis"); err == nil || !strings.Contains(err.Error(), "corrections") {
		t.Fatalf("correction limit error=%v", err)
	}
	known := controller.KnownTools()
	if len(known) != 1 || known[0] != "inspect" {
		t.Fatalf("known tools=%v", known)
	}
	if err := controller.AddIteration("unknown"); err == nil {
		t.Fatal("unknown agent received an iteration budget")
	}
	if _, err := controller.ReserveTool("unknown", "inspect"); err == nil {
		t.Fatal("unknown agent received a tool budget")
	}
}
