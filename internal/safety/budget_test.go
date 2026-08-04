package safety

import (
	"errors"
	"testing"

	"github.com/kubepilot-aiops/kubepilot/internal/domain"
)

func TestParallelToolReservationIsAtomic(t *testing.T) {
	state := &domain.AgentBudgetState{}
	limits := map[string]domain.AgentBudget{"diagnosis": {MaxIterations: 4, MaxToolUses: 3, MaxToolCost: 4, MaxTokens: 100, MaxCorrections: 2}}
	controller := NewBudgetController(state, limits, domain.AgentBudget{MaxToolUses: 3, MaxToolCost: 4, MaxTokens: 100}, map[string]int{"metric": 1, "semantic": 3})
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
	limits := map[string]domain.AgentBudget{"diagnosis": {MaxIterations: 4, MaxToolUses: 5, MaxToolCost: 8, MaxTokens: 100, MaxCorrections: 2}}
	incident := domain.AgentBudget{MaxToolUses: 8, MaxToolCost: 12, MaxTokens: 200}
	first := NewBudgetController(state, limits, incident, map[string]int{"lookup": 2})
	_, _ = first.ReserveTool("diagnosis", "lookup")
	_ = first.AddTokens("diagnosis", 25)
	remaining, err := first.UseCorrection("diagnosis")
	if err != nil || remaining != 1 {
		t.Fatalf("unexpected correction result: remaining=%d err=%v", remaining, err)
	}

	second := NewBudgetController(state, limits, incident, map[string]int{"lookup": 2})
	snapshot := second.State()
	usage := snapshot.Usage["diagnosis"]
	if usage.ToolUses != 1 || usage.ToolCost != 2 || usage.Tokens != 25 || usage.Corrections != 1 {
		t.Fatalf("resume reset budget usage: %+v", usage)
	}
}
