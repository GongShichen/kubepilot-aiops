package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"sort"
	"strings"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	toolutils "github.com/cloudwego/eino/components/tool/utils"
	"github.com/cloudwego/eino/compose"
	"github.com/kubepilot-aiops/kubepilot/internal/domain"
	"github.com/kubepilot-aiops/kubepilot/internal/safety"
	captools "github.com/kubepilot-aiops/kubepilot/tools"
	"gopkg.in/yaml.v3"
)

type RuntimePolicy struct {
	Supervisor domain.AgentBudget
	Diagnosis  domain.AgentBudget
	Recovery   domain.AgentBudget
	Incident   domain.AgentBudget
	ToolCosts  map[string]int
}

func (r *AgentRegistry) LoadToolCosts(path string) error {
	type costFile struct {
		DefaultCost int            `yaml:"default_cost"`
		Tools       map[string]int `yaml:"tools"`
	}
	raw, err := os.ReadFile(resolveProjectFile(path))
	if err != nil {
		return err
	}
	var costs costFile
	if err = yaml.Unmarshal(raw, &costs); err != nil {
		return err
	}
	for name, cost := range costs.Tools {
		if strings.TrimSpace(name) == "" || cost <= 0 {
			return fmt.Errorf("invalid tool cost policy entry")
		}
	}
	r.toolCosts = cloneToolCosts(costs.Tools)
	return nil
}

func (r *AgentRegistry) ConfigureRuntimePolicy(policy RuntimePolicy) {
	if policy.Supervisor.MaxIterations > 0 {
		r.limits[SupervisorAgentName] = policy.Supervisor
	}
	if policy.Diagnosis.MaxIterations > 0 {
		r.limits[DiagnosisAgentName] = policy.Diagnosis
	}
	if policy.Recovery.MaxIterations > 0 {
		r.limits[RecoveryAgentName] = policy.Recovery
	}
	if policy.Incident.MaxToolUses > 0 {
		r.incidentLimit = policy.Incident
	}
	if len(policy.ToolCosts) > 0 {
		r.toolCosts = cloneToolCosts(policy.ToolCosts)
	}
}

func (r *AgentRegistry) loadConstrainedDefaults() error {
	r.limits = map[string]domain.AgentBudget{
		SupervisorAgentName: {MaxIterations: 10, MaxToolUses: 8, MaxToolCost: 24, MaxTokens: 12000, MaxCorrections: 3},
		DiagnosisAgentName:  {MaxIterations: 12, MaxToolUses: 15, MaxToolCost: 32, MaxTokens: 30000, MaxCorrections: 3},
		RecoveryAgentName:   {MaxIterations: 10, MaxToolUses: 10, MaxToolCost: 16, MaxTokens: 16000, MaxCorrections: 2},
	}
	r.incidentLimit = domain.AgentBudget{MaxToolUses: 30, MaxToolCost: 72, MaxTokens: 58000}
	for _, spec := range []struct{ name, path string }{{SupervisorAgentName, "internal/agent/skills/supervisor/SKILL.md"}, {DiagnosisAgentName, "internal/agent/skills/diagnosis/SKILL.md"}, {RecoveryAgentName, "internal/agent/skills/recovery/SKILL.md"}} {
		skill, err := loadAgentSkill(resolveProjectFile(spec.path), spec.name)
		if err != nil {
			return fmt.Errorf("load %s skill: %w", spec.name, err)
		}
		if r.skills == nil {
			r.skills = map[string]agentSkill{}
		}
		r.skills[spec.name] = skill
	}
	return r.LoadToolCosts("internal/agent/skills/tool_costs.yaml")
}

func (r *AgentRegistry) SkillSnapshotHash() string {
	names := make([]string, 0, len(r.skills))
	for name := range r.skills {
		names = append(names, name)
	}
	sort.Strings(names)
	h := sha256.New()
	for _, name := range names {
		_, _ = h.Write([]byte(name + ":" + r.skills[name].Hash + "\n"))
	}
	return hex.EncodeToString(h.Sum(nil))
}

func (r *AgentRegistry) RunConstrained(ctx context.Context, state *WorkflowState, deps constrainedToolDeps) error {
	if state == nil || state.Incident == nil {
		return fmt.Errorf("workflow state and Incident are required")
	}
	state.Incident.DiagnosisLedger = &state.DiagnosisLedger
	budgetState := state.Incident.AgentBudget
	budgets := safety.NewBudgetController(budgetState, r.limits, r.incidentLimit, r.toolCosts)
	runtime := &constrainedRuntime{state: state, budgets: budgets, done: map[string]bool{}, transition: deps.Transition}
	runtime.hypotheses = safety.NewHypothesisTransitionService(&state.DiagnosisLedger, state.VerifiedHypotheses)
	defer func() { state.Incident.AgentBudget = budgets.State() }()
	ctx = withConstrainedRuntime(ctx, runtime)
	diagnosisTools, err := buildConstrainedDiagnosisTools(deps)
	if err != nil {
		return err
	}
	recoveryTools, err := buildConstrainedRecoveryTools(deps)
	if err != nil {
		return err
	}

	diagnosisMiddleware := newConstrainedAgentMiddleware(DiagnosisAgentName, r.skills[DiagnosisAgentName], "submit_diagnosis", "escalate_diagnosis")
	diagnosisAgent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{Name: DiagnosisAgentName, Description: "Investigate one Incident through evidence-driven hypothesis verification.", Instruction: "Operate as a bounded specialist. Use only the injected Skill and server-owned tool observations; finish with a structured terminal capability.", Model: r.chat, MaxIterations: r.limits[DiagnosisAgentName].MaxIterations, ToolsConfig: adk.ToolsConfig{ToolsNodeConfig: compose.ToolsNodeConfig{Tools: diagnosisTools, ExecuteSequentially: false}, EmitInternalEvents: true}, Handlers: []adk.ChatModelAgentMiddleware{diagnosisMiddleware}})
	if err != nil {
		return err
	}
	recoveryMiddleware := newConstrainedAgentMiddleware(RecoveryAgentName, r.skills[RecoveryAgentName], "accept_recovery_proposal", "escalate_recovery")
	recoveryAgent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{Name: RecoveryAgentName, Description: "Prepare and dry-run one constrained recovery proposal without mutation authority.", Instruction: "Operate only inside the proposal boundary defined by the injected Skill.", Model: r.chat, MaxIterations: r.limits[RecoveryAgentName].MaxIterations, ToolsConfig: adk.ToolsConfig{ToolsNodeConfig: compose.ToolsNodeConfig{Tools: recoveryTools, ExecuteSequentially: true}, EmitInternalEvents: true}, Handlers: []adk.ChatModelAgentMiddleware{recoveryMiddleware}})
	if err != nil {
		return err
	}

	diagnosisAgentTool := adk.NewAgentTool(ctx, diagnosisAgent)
	recoveryAgentTool := adk.NewAgentTool(ctx, recoveryAgent)
	supervisorTools, err := buildSupervisorTerminalTools()
	if err != nil {
		return err
	}
	supervisorTools = append([]tool.BaseTool{diagnosisAgentTool, recoveryAgentTool}, supervisorTools...)
	supervisorTools, err = registerConstrainedToolSet(ctx, supervisorTools, "supervisor_react", captools.CategoryDecision)
	if err != nil {
		return err
	}
	supervisorFilter := func(s *WorkflowState, name string) bool {
		if s == nil || s.Incident == nil {
			return false
		}
		switch s.Incident.Status {
		case domain.StatusNeedsAttention:
			return name == "submit_supervisor_outcome" || name == "escalate_incident"
		case domain.StatusProposing:
			if s.DryRun != nil && s.DryRun.Success {
				return name == "submit_supervisor_outcome" || name == "escalate_incident"
			}
			return name == RecoveryAgentName || name == "escalate_incident"
		default:
			return name == DiagnosisAgentName || name == "escalate_incident"
		}
	}
	supervisorMiddleware := newConstrainedAgentMiddleware(SupervisorAgentName, r.skills[SupervisorAgentName], "submit_supervisor_outcome", "escalate_incident").withToolFilter(supervisorFilter)
	supervisorAgent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{Name: SupervisorAgentName, Description: "Coordinate diagnosis and recovery specialists for one Kubernetes Incident.", Instruction: "Act as the bounded incident commander defined by the injected Skill. Specialist decisions must be delegated through AgentTools.", Model: r.chat, MaxIterations: r.limits[SupervisorAgentName].MaxIterations, ToolsConfig: adk.ToolsConfig{ToolsNodeConfig: compose.ToolsNodeConfig{Tools: supervisorTools, ExecuteSequentially: true}, EmitInternalEvents: true}, Handlers: []adk.ChatModelAgentMiddleware{supervisorMiddleware}})
	if err != nil {
		return err
	}

	payload, _ := json.Marshal(map[string]any{"incident": safeIncident(state.Incident), "workflow": WorkflowName, "objective": "coordinate an evidence-grounded diagnosis and, only after acceptance, a dry-run validated recovery proposal"})
	runner := adk.NewRunner(ctx, adk.RunnerConfig{Agent: supervisorAgent, EnableStreaming: true})
	options := []adk.AgentRunOption{
		adk.WithChatModelOptions([]model.Option{model.WithMaxTokens(r.limits[SupervisorAgentName].MaxTokens)}),
		adk.WithAgentToolRunOptions(map[string][]adk.AgentRunOption{
			DiagnosisAgentName: {adk.WithChatModelOptions([]model.Option{model.WithMaxTokens(r.limits[DiagnosisAgentName].MaxTokens)})},
			RecoveryAgentName:  {adk.WithChatModelOptions([]model.Option{model.WithMaxTokens(r.limits[RecoveryAgentName].MaxTokens)})},
		}),
	}
	if handlers, ok := ctx.Value(agentCallbacksKey{}).([]callbacks.Handler); ok && len(handlers) > 0 {
		options = append(options, adk.WithCallbacks(handlers...))
	}
	iter := runner.Query(ctx, string(payload), options...)
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		if event.Err != nil {
			return event.Err
		}
	}
	state.Incident.AgentBudget = budgets.State()
	state.DiagnosisLedger.AgentDecisions = append([]domain.AgentDecisionEvent(nil), state.DiagnosisLedger.AgentDecisions...)
	if !runtime.isDone(SupervisorAgentName) {
		return fmt.Errorf("Supervisor Agent ended without a validated terminal outcome")
	}
	return nil
}

type supervisorOutcome struct {
	Status string `json:"status"`
	Reason string `json:"reason"`
}

func buildSupervisorTerminalTools() ([]tool.BaseTool, error) {
	submit, err := toolutils.InferTool("submit_supervisor_outcome", "Submit the current server-validated diagnosis and proposal outcome.", func(ctx context.Context, in supervisorOutcome) (constrainedToolOutput, error) {
		runtime, err := runtimeFromContext(ctx)
		if err != nil {
			return constrainedToolOutput{}, err
		}
		runtime.mu.Lock()
		defer runtime.mu.Unlock()
		valid := runtime.state.Incident.Status == domain.StatusNeedsAttention || (runtime.state.Incident.Status == domain.StatusProposing && runtime.state.Incident.Proposal != nil && runtime.state.DryRun != nil && runtime.state.DryRun.Success)
		if !valid {
			return safetyObservationLocked(ctx, runtime, SupervisorAgentName, domain.SafetyScopeSupervisor, "specialist_outcome_incomplete", "the specialist outcome does not yet satisfy the deterministic handoff boundary", []string{"an accepted diagnosis and a fresh dry-run proposal, or an explicit human-required state, is required"})
		}
		runtime.markDoneLocked(SupervisorAgentName)
		return constrainedToolOutput{OK: true, Message: strings.TrimSpace(in.Reason)}, nil
	})
	if err != nil {
		return nil, err
	}
	escalate, err := toolutils.InferTool("escalate_incident", "Request human attention without changing recovery authority.", func(ctx context.Context, in supervisorOutcome) (constrainedToolOutput, error) {
		runtime, err := runtimeFromContext(ctx)
		if err != nil {
			return constrainedToolOutput{}, err
		}
		runtime.mu.Lock()
		defer runtime.mu.Unlock()
		if runtime.state.Incident.Status != domain.StatusNeedsAttention {
			_ = runtime.transitionIncident(ctx, domain.StatusNeedsAttention)
		}
		feedback := safety.HumanRequired(domain.SafetyScopeSupervisor, "supervisor_escalated", firstNonBlank(in.Reason, "the Incident requires human attention"))
		runtime.state.DiagnosisLedger.SafetyFeedback = append(runtime.state.DiagnosisLedger.SafetyFeedback, feedback)
		runtime.markDoneLocked(SupervisorAgentName)
		return constrainedToolOutput{Feedback: &feedback}, nil
	})
	if err != nil {
		return nil, err
	}
	return []tool.BaseTool{submit, escalate}, nil
}

func cloneToolCosts(in map[string]int) map[string]int {
	out := make(map[string]int, len(in))
	for name, cost := range in {
		out[name] = cost
	}
	return out
}
func firstNonBlank(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return "unspecified"
}

func resolveProjectFile(path string) string {
	for _, candidate := range []string{path, "../" + path} {
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	_, file, _, ok := runtime.Caller(0)
	if ok {
		return strings.TrimSuffix(file, "/agent/constrained_agents.go") + "/" + path
	}
	return path
}
