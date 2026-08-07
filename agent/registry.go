package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/kubepilot-aiops/kubepilot/internal/domain"
	"github.com/kubepilot-aiops/kubepilot/internal/safety"
	captools "github.com/kubepilot-aiops/kubepilot/tools"
)

const (
	SupervisorAgentName  = "supervisor_agent"
	PlannerAgentName     = "planner_agent"
	MetricWorkerName     = "metric_worker"
	LogWorkerName        = "log_worker"
	TraceWorkerName      = "trace_worker"
	TopologyWorkerName   = "topology_worker"
	DiagnosisAgentName   = "diagnosis_agent"
	AlternativeAgentName = "alternative_agent"
	CriticAgentName      = "critic_agent"
	CognitiveRuntimeName = "cognitive_runtime"
	RecoveryAgentName    = "recovery_agent"
)

// AgentRegistry is the single registration point for KubePilot's bounded roles.
type AgentRegistry struct {
	chat                     model.BaseChatModel
	skills                   map[string]agentSkill
	limits                   map[string]domain.AgentBudget
	toolCosts                map[string]int
	requestMaxTokens         int
	modelMaxRetries          int
	inputPricePerMillion     float64
	outputPricePerMillion    float64
	reasoningPricePerMillion float64
}

func (r *AgentRegistry) Names() []string {
	return []string{SupervisorAgentName, PlannerAgentName, MetricWorkerName, LogWorkerName, TraceWorkerName, TopologyWorkerName, DiagnosisAgentName, AlternativeAgentName, CriticAgentName, CognitiveRuntimeName, RecoveryAgentName}
}

func (r *AgentRegistry) ProceduralMemories() []domain.MemoryResult {
	names := r.Names()
	results := make([]domain.MemoryResult, 0, len(names))
	for _, name := range names {
		skill, ok := r.skills[name]
		if !ok {
			continue
		}
		results = append(results, domain.MemoryResult{ID: name, Kind: domain.MemoryProcedural, Summary: skill.FrontMatter.Description, Score: 1, Version: skill.Hash, Provenance: map[string]any{"skill_hash": skill.Hash}})
	}
	return results
}

type agentCallbacksKey struct{}

func withAgentCallbacks(ctx context.Context, handlers ...callbacks.Handler) context.Context {
	return context.WithValue(ctx, agentCallbacksKey{}, handlers)
}

func NewAgentRegistry(ctx context.Context, chat model.BaseChatModel) (*AgentRegistry, error) {
	if chat == nil {
		return nil, fmt.Errorf("Eino chat model is required")
	}
	out := &AgentRegistry{chat: chat}
	if err := out.loadConstrainedDefaults(); err != nil {
		return nil, err
	}
	return out, nil
}

// CorrelateWithCandidateCapability lets the Supervisor model decide when to query
// the repository and when the available observations justify a grouping
// decision. No application code constructs a business ToolCall.
func (r *AgentRegistry) CorrelateWithCandidateCapability(ctx context.Context, alert domain.Alert, service, namespace, resource string, candidateCapability captools.Capability) (string, error) {
	if candidateCapability == nil {
		return "", fmt.Errorf("candidate query tool is required")
	}
	registration := captools.Registration{Category: captools.CategoryDecision, AllowedNodes: []string{captools.NodeAlertCorrelation}, Timeout: 30 * time.Second, MaxArgumentBytes: 64 << 10, MaxOutputBytes: 64 << 10}
	decisionCapability, err := captools.NewCapability("submit_correlation_decision", "Submit the final structured alert-correlation decision.", func(_ context.Context, in CorrelationDecision) (CorrelationDecision, error) {
		if in.Confidence < 0 || in.Confidence > 1 || strings.TrimSpace(in.Reason) == "" || (in.Merge && in.IncidentID == "") {
			return CorrelationDecision{}, fmt.Errorf("invalid correlation decision")
		}
		return in, nil
	}, registration)
	if err != nil {
		return "", err
	}
	capabilityRegistry := captools.NewRegistry()
	if err = capabilityRegistry.RegisterAll(ctx, candidateCapability, decisionCapability); err != nil {
		return "", err
	}
	toolsNodeConfig, err := capabilityRegistry.ToolsNodeConfig(captools.NodeAlertCorrelation, true)
	if err != nil {
		return "", err
	}
	budgetState := &domain.AgentBudgetState{}
	budgets := safety.NewBudgetController(budgetState, r.limits, r.toolCosts)
	state := &WorkflowState{Workflow: WorkflowName, Incident: &domain.Incident{Namespace: namespace, Service: service, Resource: resource, AgentBudget: budgetState}}
	runtime := &constrainedRuntime{state: state, budgets: budgets, done: map[string]bool{}}
	ctx = withConstrainedRuntime(ctx, runtime)
	middleware := newConstrainedAgentMiddleware(SupervisorAgentName, r.skills[SupervisorAgentName], "submit_correlation_decision")
	agentInstance, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{Name: SupervisorAgentName, Description: "Use operational metadata to correlate one alert.", Instruction: "Act as the bounded Supervisor. For this pre-intake task, decide correlation only from tool-returned operational metadata and finish with the correlation decision capability.", Model: r.chat, MaxIterations: r.limits[SupervisorAgentName].MaxIterations, ModelRetryConfig: r.modelRetryConfig(), ToolsConfig: adk.ToolsConfig{ToolsNodeConfig: toolsNodeConfig, ReturnDirectly: map[string]bool{"submit_correlation_decision": true}}, Handlers: []adk.ChatModelAgentMiddleware{middleware}})
	if err != nil {
		return "", err
	}
	payload, _ := json.Marshal(map[string]any{"alert": map[string]any{"name": alert.Name, "starts_at": alert.StartsAt, "labels": alert.Labels}, "service": service, "namespace": namespace, "resource": resource, "candidate_query_constraints": map[string]any{"namespace": namespace, "limit": 100}})
	runner := adk.NewRunner(ctx, adk.RunnerConfig{Agent: agentInstance, EnableStreaming: true})
	options := make([]adk.AgentRunOption, 0, 1)
	if r.requestMaxTokens > 0 {
		options = append(options, adk.WithChatModelOptions([]model.Option{model.WithMaxTokens(r.requestMaxTokens)}))
	}
	if handlers, ok := ctx.Value(agentCallbacksKey{}).([]callbacks.Handler); ok && len(handlers) > 0 {
		options = append(options, adk.WithCallbacks(handlers...))
	}
	// The Agent run has no role-level wall-clock deadline. Individual model
	// requests and capabilities retain their own transport/operation timeouts.
	iter := runner.Query(ctx, string(payload), options...)
	var raw string
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		if event.Err != nil {
			return "", event.Err
		}
		if event.Output == nil || event.Output.MessageOutput == nil {
			continue
		}
		message, getErr := event.Output.MessageOutput.GetMessage()
		if getErr != nil {
			return "", getErr
		}
		if message != nil && message.Role == schema.Tool && message.ToolName == "submit_correlation_decision" {
			raw = message.Content
		}
	}
	if raw == "" {
		return "", fmt.Errorf("Supervisor correlation ended without a structured decision")
	}
	var decision CorrelationDecision
	if err = json.Unmarshal([]byte(raw), &decision); err != nil {
		return "", err
	}
	if !decision.Merge || decision.Confidence < .8 {
		return "", nil
	}
	return decision.IncidentID, nil
}
