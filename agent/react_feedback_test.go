package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/kubepilot-aiops/kubepilot/internal/domain"
	"github.com/kubepilot-aiops/kubepilot/internal/safety"
	captools "github.com/kubepilot-aiops/kubepilot/tools"
)

type reflectingDiagnosisModel struct {
	feedbackSeen bool
	validSubmit  bool
	calls        int
	observed     bool
}

func (m *reflectingDiagnosisModel) Generate(_ context.Context, messages []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	m.calls++
	if m.calls > 1 {
		m.feedbackSeen = true
	}
	for _, message := range messages {
		if message != nil && (message.Role == schema.Tool || strings.Contains(message.Content, "hypothesis_structure_invalid")) {
			m.observed = true
		}
	}
	available := map[string]bool{}
	for _, info := range model.GetCommonOptions(nil, opts...).Tools {
		available[info.Name] = true
	}
	if available["submit_hypotheses"] && !m.validSubmit {
		ids := []string{"unknown-evidence"}
		if m.feedbackSeen {
			ids = []string{"e1"}
			m.validSubmit = true
		}
		payload, _ := json.Marshal(HypothesisSubmission{ReasoningType: "hypothesis_verification", Hypotheses: []domain.HypothesisDraft{{ID: "h1", Category: "cpu", Variant: "busy_loop", Cause: "CPU saturation", Service: "gateway", Resource: "gateway", PriorProbability: .5, SupportingEvidenceIDs: ids, ExpectedCausalPath: []string{"cpu", "error"}}}})
		return &schema.Message{Role: schema.Assistant, ToolCalls: []schema.ToolCall{{ID: "submit-" + string(ids[0]), Type: "function", Function: schema.FunctionCall{Name: "submit_hypotheses", Arguments: string(payload)}}}, ResponseMeta: &schema.ResponseMeta{Usage: &schema.TokenUsage{TotalTokens: 10}}}, nil
	}
	// A terminal escalation is enough to end this isolated specialist run; the
	// assertion below is about the same Agent receiving and acting on feedback.
	payload, _ := json.Marshal(emptyToolInput{})
	return &schema.Message{Role: schema.Assistant, ToolCalls: []schema.ToolCall{{ID: "escalate", Type: "function", Function: schema.FunctionCall{Name: "escalate_diagnosis", Arguments: string(payload)}}}}, nil
}

func (m *reflectingDiagnosisModel) Stream(ctx context.Context, messages []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	message, err := m.Generate(ctx, messages, opts...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{message}), nil
}

func TestSameReActAgentConsumesSafetyFeedbackAndReplans(t *testing.T) {
	ctx := context.Background()
	state := &WorkflowState{Incident: &domain.Incident{ID: "feedback-incident", Namespace: "kubepilot-demo", Service: "gateway", Resource: "gateway", Evidence: []domain.Evidence{{ID: "e1", Source: "kubernetes"}}, AgentBudget: &domain.AgentBudgetState{}}}
	budget := safety.NewBudgetController(state.Incident.AgentBudget, map[string]domain.AgentBudget{DiagnosisAgentName: {MaxIterations: 5, MaxToolUses: 5, MaxTokens: 10000, MaxCorrections: 2}}, map[string]int{"submit_hypotheses": 1, "escalate_diagnosis": 1})
	runtime := &constrainedRuntime{state: state, budgets: budget, done: map[string]bool{}, hypotheses: safety.NewHypothesisTransitionService(&state.DiagnosisLedger, nil)}
	runCtx := withConstrainedRuntime(ctx, runtime)
	submit, err := captools.NewCapability("submit_hypotheses", "Submit hypotheses for verification.", func(callCtx context.Context, input HypothesisSubmission) (constrainedToolOutput, error) {
		return recordHypotheses(callCtx, input)
	}, constrainedRegistration(captools.CategoryDecision, captools.NodeDiagnosisReact))
	if err != nil {
		t.Fatal(err)
	}
	escalate, err := captools.NewCapability("escalate_diagnosis", "Escalate when the specialist cannot proceed.", func(callCtx context.Context, _ emptyToolInput) (constrainedToolOutput, error) {
		runtime, runtimeErr := runtimeFromContext(callCtx)
		if runtimeErr != nil {
			return constrainedToolOutput{}, runtimeErr
		}
		runtime.markDone(DiagnosisAgentName)
		return constrainedToolOutput{OK: true}, nil
	}, constrainedRegistration(captools.CategoryDecision, captools.NodeDiagnosisReact))
	if err != nil {
		t.Fatal(err)
	}
	capabilityRegistry := captools.NewRegistry()
	if err = capabilityRegistry.RegisterAll(ctx, submit, escalate); err != nil {
		t.Fatal(err)
	}
	toolsNodeConfig, err := capabilityRegistry.ToolsNodeConfig(captools.NodeDiagnosisReact, true)
	if err != nil {
		t.Fatal(err)
	}
	modelClient := &reflectingDiagnosisModel{}
	middleware := newConstrainedAgentMiddleware(DiagnosisAgentName, agentSkill{Content: "# Mission\n# Boundaries\n# Decision criteria\n# Output"}, "escalate_diagnosis")
	specialist, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{Name: DiagnosisAgentName, Model: modelClient, MaxIterations: 5, ToolsConfig: adk.ToolsConfig{ToolsNodeConfig: toolsNodeConfig}, Handlers: []adk.ChatModelAgentMiddleware{middleware}})
	if err != nil {
		t.Fatal(err)
	}
	runner := adk.NewRunner(runCtx, adk.RunnerConfig{Agent: specialist, EnableStreaming: true})
	iter := runner.Query(runCtx, "investigate the incident", adk.WithChatModelOptions([]model.Option{model.WithMaxTokens(10000)}))
	var runErr error
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		if event.Err != nil {
			runErr = event.Err
			break
		}
	}
	if runErr != nil && !modelClient.validSubmit {
		t.Fatalf("ADK feedback loop failed after %d model calls: %v", modelClient.calls, runErr)
	}
	if !modelClient.feedbackSeen || !modelClient.observed || !modelClient.validSubmit {
		t.Fatalf("same ADK Agent did not consume feedback and replan: seen=%v observed=%v valid=%v", modelClient.feedbackSeen, modelClient.observed, modelClient.validSubmit)
	}
	if len(state.DiagnosisLedger.SafetyFeedback) == 0 {
		t.Fatal("invalid submission did not create structured SafetyFeedback")
	}
	if len(state.HypothesisDrafts) != 1 || state.HypothesisDrafts[0].SupportingEvidenceIDs[0] != "e1" {
		t.Fatalf("corrected hypothesis did not reach ledger: %+v", state.HypothesisDrafts)
	}
}
