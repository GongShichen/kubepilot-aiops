package agent

import (
	"testing"

	"github.com/cloudwego/eino/schema"
	"github.com/kubepilot-aiops/kubepilot/internal/domain"
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
