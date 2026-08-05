package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"github.com/kubepilot-aiops/kubepilot/internal/domain"
	"github.com/kubepilot-aiops/kubepilot/internal/safety"
	"gopkg.in/yaml.v3"
)

type skillFrontMatter struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	Agent       string `yaml:"agent"`
}

type agentSkill struct {
	FrontMatter skillFrontMatter
	Content     string
	Hash        string
}

func loadAgentSkill(path, expectedAgent string) (agentSkill, error) {
	raw, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return agentSkill{}, err
	}
	text := string(raw)
	if !strings.HasPrefix(text, "---\n") {
		return agentSkill{}, fmt.Errorf("skill %s has no YAML front matter", path)
	}
	parts := strings.SplitN(strings.TrimPrefix(text, "---\n"), "\n---\n", 2)
	if len(parts) != 2 {
		return agentSkill{}, fmt.Errorf("skill %s has invalid YAML front matter", path)
	}
	var front skillFrontMatter
	if err = yaml.Unmarshal([]byte(parts[0]), &front); err != nil {
		return agentSkill{}, err
	}
	if front.Name == "" || front.Description == "" || front.Agent != expectedAgent {
		return agentSkill{}, fmt.Errorf("skill %s does not belong to %s", path, expectedAgent)
	}
	content := strings.TrimSpace(parts[1])
	for _, section := range []string{"# Mission", "# Boundaries", "# Decision criteria", "# Output"} {
		if !strings.Contains(content, section) {
			return agentSkill{}, fmt.Errorf("skill %s is missing %s", path, section)
		}
	}
	lower := strings.ToLower(content)
	for _, forbidden := range []string{"scenario_id", "case_id", "ground_truth", "benchmark/", "benchmark\\"} {
		if strings.Contains(lower, forbidden) {
			return agentSkill{}, fmt.Errorf("skill %s contains forbidden hidden-workflow content", path)
		}
	}
	hash := sha256.Sum256(raw)
	return agentSkill{FrontMatter: front, Content: content, Hash: hex.EncodeToString(hash[:])}, nil
}

type runtimeContextKey struct{}

type constrainedRuntime struct {
	mu              sync.Mutex
	state           *WorkflowState
	budgets         *safety.BudgetController
	done            map[string]bool
	reservedCallIDs map[string]bool
	transition      func(context.Context, *domain.Incident, domain.IncidentStatus) error
	hypotheses      *safety.HypothesisTransitionService
}

func (r *constrainedRuntime) transitionIncident(ctx context.Context, to domain.IncidentStatus) error {
	if r.transition != nil {
		return r.transition(ctx, r.state.Incident, to)
	}
	return domain.Transition(r.state.Incident, to)
}

func (r *constrainedRuntime) reserveCallIDs(ids []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.reservedCallIDs == nil {
		r.reservedCallIDs = map[string]bool{}
	}
	for _, id := range ids {
		r.reservedCallIDs[id] = true
	}
}
func (r *constrainedRuntime) consumeReservedCall(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.reservedCallIDs[id] {
		return false
	}
	delete(r.reservedCallIDs, id)
	return true
}

func (r *constrainedRuntime) markDone(agent string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.markDoneLocked(agent)
}

func (r *constrainedRuntime) markDoneLocked(agent string) {
	if r.done == nil {
		r.done = map[string]bool{}
	}
	r.done[agent] = true
}

func (r *constrainedRuntime) isDone(agent string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.done != nil && r.done[agent]
}

func withConstrainedRuntime(ctx context.Context, runtime *constrainedRuntime) context.Context {
	return context.WithValue(ctx, runtimeContextKey{}, runtime)
}

func runtimeFromContext(ctx context.Context) (*constrainedRuntime, error) {
	runtime, ok := ctx.Value(runtimeContextKey{}).(*constrainedRuntime)
	if !ok || runtime == nil || runtime.state == nil || runtime.budgets == nil {
		return nil, fmt.Errorf("constrained agent runtime is unavailable")
	}
	return runtime, nil
}

type constrainedAgentMiddleware struct {
	*adk.BaseChatModelAgentMiddleware
	agent         string
	skill         agentSkill
	terminalTools map[string]bool
	toolFilter    func(*WorkflowState, string) bool
	allToolInfos  []*schema.ToolInfo
}

func (m *constrainedAgentMiddleware) withToolFilter(filter func(*WorkflowState, string) bool) *constrainedAgentMiddleware {
	m.toolFilter = filter
	return m
}

func newConstrainedAgentMiddleware(agent string, skill agentSkill, terminalTools ...string) *constrainedAgentMiddleware {
	terminal := map[string]bool{}
	for _, name := range terminalTools {
		terminal[name] = true
	}
	return &constrainedAgentMiddleware{BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{}, agent: agent, skill: skill, terminalTools: terminal}
}

func (m *constrainedAgentMiddleware) BeforeAgent(ctx context.Context, runCtx *adk.ChatModelAgentContext) (context.Context, *adk.ChatModelAgentContext, error) {
	runtime, err := runtimeFromContext(ctx)
	if err != nil {
		return ctx, runCtx, err
	}
	remaining := runtime.budgets.RemainingTools(m.agent)
	runCtx.Instruction = strings.TrimSpace(runCtx.Instruction + "\n\n" + m.skill.Content + fmt.Sprintf("\n\nCurrent operational tool uses remaining: %d. The injected skill is authoritative for role and output boundaries.", remaining))
	return ctx, runCtx, nil
}

func (m *constrainedAgentMiddleware) BeforeModelRewriteState(ctx context.Context, state *adk.ChatModelAgentState, mc *adk.ModelContext) (context.Context, *adk.ChatModelAgentState, error) {
	runtime, err := runtimeFromContext(ctx)
	if err != nil {
		return ctx, state, err
	}
	if err = runtime.budgets.AddIteration(m.agent); err != nil {
		return ctx, state, err
	}
	if m.allToolInfos == nil {
		m.allToolInfos = append([]*schema.ToolInfo(nil), state.ToolInfos...)
	} else {
		state.ToolInfos = append([]*schema.ToolInfo(nil), m.allToolInfos...)
	}
	if runtime.isDone(m.agent) {
		state.ToolInfos = nil
		state.DeferredToolInfos = nil
		return ctx, state, nil
	}
	if m.toolFilter != nil {
		filtered := make([]*schema.ToolInfo, 0, len(state.ToolInfos))
		for _, info := range state.ToolInfos {
			if info != nil && m.toolFilter(runtime.state, info.Name) {
				filtered = append(filtered, info)
			}
		}
		state.ToolInfos = filtered
	}
	if runtime.budgets.RemainingTools(m.agent) <= 1 {
		filtered := make([]*schema.ToolInfo, 0, len(state.ToolInfos))
		for _, info := range state.ToolInfos {
			if info != nil && m.terminalTools[info.Name] {
				filtered = append(filtered, info)
			}
		}
		state.ToolInfos = filtered
	}
	return ctx, state, nil
}

func (m *constrainedAgentMiddleware) AfterModelRewriteState(ctx context.Context, state *adk.ChatModelAgentState, mc *adk.ModelContext) (context.Context, *adk.ChatModelAgentState, error) {
	runtime, err := runtimeFromContext(ctx)
	if err != nil || len(state.Messages) == 0 {
		return ctx, state, err
	}
	last := state.Messages[len(state.Messages)-1]
	if last != nil {
		tokens := modelTokensForBudget(last)
		if err = runtime.budgets.AddTokens(m.agent, tokens); err != nil {
			return ctx, state, err
		}
	}
	if last == nil || len(last.ToolCalls) == 0 {
		return ctx, state, nil
	}
	names := make([]string, 0, len(last.ToolCalls))
	ids := make([]string, 0, len(last.ToolCalls))
	for _, call := range last.ToolCalls {
		names = append(names, call.Function.Name)
		ids = append(ids, call.ID)
	}
	if _, err = runtime.budgets.ReserveTools(m.agent, names); err != nil {
		return ctx, state, err
	}
	runtime.reserveCallIDs(ids)
	usage := runtime.budgets.State().Usage[m.agent]
	for _, call := range last.ToolCalls {
		runtime.state.DiagnosisLedger.AgentDecisions = append(runtime.state.DiagnosisLedger.AgentDecisions, domain.AgentDecisionEvent{AgentID: m.agent, Iteration: usage.Iterations, ObservationSummary: latestObservation(state.Messages), SelectedAction: call.Function.Name, ReasonCategory: decisionCategory(call.Function.Name), OccurredAt: time.Now().UTC()})
	}
	return ctx, state, nil
}

// modelTokensForBudget returns tokens generated by the current model turn.
// Prompt tokens are deliberately excluded: they are sent again on every
// ReAct turn as part of the conversation history and charging them against
// the exploration budget makes the budget depend on history length rather
// than model work. CompletionTokens includes provider-reported reasoning
// tokens when the provider exposes them. If usage is unavailable, estimate
// only the new assistant message instead of recounting the full conversation.
func modelTokensForBudget(message *schema.Message) int {
	if message == nil {
		return 0
	}
	if message.ResponseMeta != nil && message.ResponseMeta.Usage != nil {
		usage := message.ResponseMeta.Usage
		if usage.CompletionTokens > 0 {
			return usage.CompletionTokens
		}
		if usage.TotalTokens > usage.PromptTokens {
			return usage.TotalTokens - usage.PromptTokens
		}
		if usage.TotalTokens > 0 {
			return usage.TotalTokens
		}
	}
	characters := utf8.RuneCountInString(message.Content)
	for _, call := range message.ToolCalls {
		characters += utf8.RuneCountInString(call.Function.Name) + utf8.RuneCountInString(call.Function.Arguments)
	}
	if characters == 0 {
		return 0
	}
	return (characters + 2) / 3
}

func (m *constrainedAgentMiddleware) WrapInvokableToolCall(ctx context.Context, endpoint adk.InvokableToolCallEndpoint, toolContext *adk.ToolContext) (adk.InvokableToolCallEndpoint, error) {
	name := toolContext.Name
	return func(callCtx context.Context, arguments string, opts ...tool.Option) (string, error) {
		runtime, err := runtimeFromContext(callCtx)
		if err != nil {
			return "", err
		}
		if !runtime.consumeReservedCall(toolContext.CallID) {
			_, err = runtime.budgets.ReserveTool(m.agent, name)
			if err != nil {
				return "", err
			}
		}
		if len(arguments) > 64<<10 {
			return "", fmt.Errorf("tool arguments exceed the bounded capability limit")
		}
		if code := forbiddenToolIntent(m.agent, arguments); code != "" {
			runtime.mu.Lock()
			feedback := safety.Fatal(safetyScopeForAgent(m.agent), code, "the requested capability input crosses a non-negotiable safety boundary")
			runtime.state.DiagnosisLedger.SafetyFeedback = append(runtime.state.DiagnosisLedger.SafetyFeedback, feedback)
			_ = runtime.transitionIncident(callCtx, domain.StatusNeedsAttention)
			runtime.markDoneLocked(m.agent)
			runtime.mu.Unlock()
			payload, _ := json.Marshal(constrainedToolOutput{Feedback: &feedback})
			return string(payload), nil
		}
		timeout := 30 * time.Second
		if name == DiagnosisAgentName || name == RecoveryAgentName {
			timeout = 2 * time.Minute
		}
		boundedCtx, cancel := context.WithTimeout(callCtx, timeout)
		defer cancel()
		output, err := endpoint(boundedCtx, arguments, opts...)
		if err != nil {
			return "", err
		}
		if len(output) > 2<<20 {
			return "", fmt.Errorf("tool output exceeds the bounded capability limit")
		}
		return output, nil
	}, nil
}

func forbiddenToolIntent(agent, arguments string) string {
	lower := strings.ToLower(arguments)
	for _, marker := range []string{"scenario_id", "case_id", "ground_truth", "allowed_answer", "benchmark/", "benchmark\\"} {
		if strings.Contains(lower, marker) {
			return "evaluation_data_access_forbidden"
		}
	}
	if agent == RecoveryAgentName {
		for _, marker := range []string{"execution_context", "approval_id", "mutation_spec_hash", "idempotency_key"} {
			if strings.Contains(lower, marker) {
				return "server_context_override_forbidden"
			}
		}
	}
	return ""
}

func safetyScopeForAgent(agent string) domain.SafetyScope {
	switch agent {
	case DiagnosisAgentName:
		return domain.SafetyScopeDiagnosis
	case RecoveryAgentName:
		return domain.SafetyScopeRecoveryProposal
	default:
		return domain.SafetyScopeSupervisor
	}
}

func latestObservation(messages []*schema.Message) string {
	for index := len(messages) - 2; index >= 0; index-- {
		if messages[index] != nil && messages[index].Role == schema.Tool {
			name := messages[index].ToolName
			if name == "" {
				name = "tool"
			}
			return "received structured result from " + name
		}
	}
	return "incident context received"
}

func decisionCategory(name string) string {
	switch {
	case name == DiagnosisAgentName || name == RecoveryAgentName:
		return "agent_delegation"
	case strings.HasPrefix(name, "query_"):
		return "evidence_collection"
	case strings.HasPrefix(name, "retrieve_") || strings.HasPrefix(name, "rerank_") || strings.HasPrefix(name, "fuse_"):
		return "historical_reasoning"
	case strings.Contains(name, "hypothes") || strings.Contains(name, "diagnosis"):
		return "hypothesis_reasoning"
	case strings.Contains(name, "recovery") || strings.HasPrefix(name, "dry_run_"):
		return "recovery_planning"
	default:
		return "incident_coordination"
	}
}
