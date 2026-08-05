package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
	"github.com/kubepilot-aiops/kubepilot/internal/domain"
	"github.com/kubepilot-aiops/kubepilot/internal/safety"
)

func TestModelTokensForBudgetUsesGeneratedTokens(t *testing.T) {
	message := &schema.Message{
		ResponseMeta: &schema.ResponseMeta{Usage: &schema.TokenUsage{
			PromptTokens:     1200,
			CompletionTokens: 37,
			TotalTokens:      1237,
		}},
	}
	if got := modelTokensForBudget(message); got != 37 {
		t.Fatalf("model token budget charged prompt tokens: got %d, want 37", got)
	}
}

func TestModelTokensForBudgetFallsBackToCurrentMessage(t *testing.T) {
	current := &schema.Message{Content: "abcd"}
	if got := modelTokensForBudget(current); got != 2 {
		t.Fatalf("fallback token estimate used unexpected history: got %d, want 2", got)
	}
}

func TestRuntimePolicySeparatesRequestLimitFromCumulativeBudget(t *testing.T) {
	registry := &AgentRegistry{limits: map[string]domain.AgentBudget{}}
	registry.ConfigureRuntimePolicy(RuntimePolicy{
		Diagnosis:        domain.AgentBudget{MaxIterations: 12, MaxTokens: 30000},
		RequestMaxTokens: 4096,
	})
	if registry.limits[DiagnosisAgentName].MaxTokens != 30000 {
		t.Fatalf("cumulative budget changed: %+v", registry.limits[DiagnosisAgentName])
	}
	if registry.requestMaxTokens != 4096 {
		t.Fatalf("request token limit not retained: got %d", registry.requestMaxTokens)
	}
}

func TestModelRetryPolicyDefaultsToThreeAttempts(t *testing.T) {
	registry := &AgentRegistry{}
	if got := registry.modelRetryConfig().MaxRetries; got != 3 {
		t.Fatalf("default model retries=%d, want 3", got)
	}
	registry.ConfigureRuntimePolicy(RuntimePolicy{ModelMaxRetries: 2})
	if got := registry.modelRetryConfig().MaxRetries; got != 2 {
		t.Fatalf("configured model retries=%d, want 2", got)
	}
}

func TestBudgetMessageTracksCurrentAgentUsage(t *testing.T) {
	state := &adk.ChatModelAgentState{Messages: []*schema.Message{{Role: schema.User, Content: "incident"}}}
	updateAgentBudgetMessage(state, DiagnosisAgentName, 50)
	updateAgentBudgetMessage(state, DiagnosisAgentName, 37)
	count := 0
	for _, message := range state.Messages {
		if message != nil && strings.HasPrefix(message.Content, budgetMessagePrefix) {
			count++
			if !strings.Contains(message.Content, "remaining_tool_uses=37") {
				t.Fatalf("budget snapshot was not refreshed: %q", message.Content)
			}
		}
	}
	if count != 1 {
		t.Fatalf("budget snapshots accumulated in model context: %d", count)
	}
}

func TestBudgetExhaustionBecomesHumanRequired(t *testing.T) {
	incident := &domain.Incident{ID: "budget-incident", Status: domain.StatusDiagnosing}
	runtime := &constrainedRuntime{
		state:   &WorkflowState{Incident: incident},
		budgets: safety.NewBudgetController(&domain.AgentBudgetState{}, map[string]domain.AgentBudget{DiagnosisAgentName: {MaxToolUses: 50, MaxTokens: 1000}}, nil),
		done:    map[string]bool{},
	}
	handled, err := handleAgentBudgetExhaustion(context.Background(), runtime, fmt.Errorf("node failed: %w", safety.ErrBudgetExceeded{Agent: DiagnosisAgentName, Tool: "query"}))
	if err != nil || !handled {
		t.Fatalf("budget exhaustion was not handled: handled=%v err=%v", handled, err)
	}
	if incident.Status != domain.StatusNeedsAttention {
		t.Fatalf("incident status=%s, want %s", incident.Status, domain.StatusNeedsAttention)
	}
	if len(runtime.state.DiagnosisLedger.SafetyFeedback) != 1 || runtime.state.DiagnosisLedger.SafetyFeedback[0].Category != domain.SafetyHumanRequired {
		t.Fatalf("missing human-required feedback: %+v", runtime.state.DiagnosisLedger.SafetyFeedback)
	}
}
