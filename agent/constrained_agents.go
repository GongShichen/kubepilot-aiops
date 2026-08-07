package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/kubepilot-aiops/kubepilot/internal/domain"
	"github.com/kubepilot-aiops/kubepilot/internal/safety"
	captools "github.com/kubepilot-aiops/kubepilot/tools"
	"gopkg.in/yaml.v3"
)

type RuntimePolicy struct {
	Supervisor domain.AgentBudget
	Diagnosis  domain.AgentBudget
	Recovery   domain.AgentBudget
	ToolCosts  map[string]int
	// RequestMaxTokens limits one Agent model response. AgentBudget.MaxTokens
	// independently enforces the same generated-output boundary after usage is
	// reported; input and cumulative totals are telemetry only.
	RequestMaxTokens int
	// ModelMaxRetries is the number of retries Eino performs for every
	// Generate/Stream request. A value of three means three retries after the
	// initial request (four total attempts).
	ModelMaxRetries          int
	InputPricePerMillion     float64
	OutputPricePerMillion    float64
	ReasoningPricePerMillion float64
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
		for _, name := range diagnosisAgentNames() {
			r.limits[name] = policy.Diagnosis
		}
	}
	if policy.Recovery.MaxIterations > 0 {
		r.limits[RecoveryAgentName] = policy.Recovery
	}
	if len(policy.ToolCosts) > 0 {
		r.toolCosts = cloneToolCosts(policy.ToolCosts)
	}
	if policy.RequestMaxTokens > 0 {
		r.requestMaxTokens = policy.RequestMaxTokens
	}
	if policy.ModelMaxRetries >= 0 {
		r.modelMaxRetries = policy.ModelMaxRetries
	}
	r.inputPricePerMillion = policy.InputPricePerMillion
	r.outputPricePerMillion = policy.OutputPricePerMillion
	r.reasoningPricePerMillion = policy.ReasoningPricePerMillion
}

func (r *AgentRegistry) loadConstrainedDefaults() error {
	r.limits = map[string]domain.AgentBudget{
		SupervisorAgentName: {MaxIterations: 10, MaxToolUses: 50, MaxTokens: 8192, MaxCorrections: 3},
		RecoveryAgentName:   {MaxIterations: 10, MaxToolUses: 50, MaxTokens: 8192, MaxCorrections: 2},
	}
	diagnosisBudget := domain.AgentBudget{MaxIterations: 18, MaxToolUses: 50, MaxTokens: 8192, MaxCorrections: 3}
	for _, name := range diagnosisAgentNames() {
		r.limits[name] = diagnosisBudget
	}
	r.modelMaxRetries = 3
	for _, spec := range []struct{ name, path string }{
		{SupervisorAgentName, "internal/agent/skills/supervisor/SKILL.md"},
		{PlannerAgentName, "internal/agent/skills/planner/SKILL.md"},
		{MetricWorkerName, "internal/agent/skills/metric-worker/SKILL.md"},
		{LogWorkerName, "internal/agent/skills/log-worker/SKILL.md"},
		{TraceWorkerName, "internal/agent/skills/trace-worker/SKILL.md"},
		{TopologyWorkerName, "internal/agent/skills/topology-worker/SKILL.md"},
		{DiagnosisAgentName, "internal/agent/skills/diagnosis/SKILL.md"},
		{AlternativeAgentName, "internal/agent/skills/alternative/SKILL.md"},
		{CriticAgentName, "internal/agent/skills/critic/SKILL.md"},
		{CognitiveRuntimeName, "internal/agent/skills/cognitive/SKILL.md"},
		{RecoveryAgentName, "internal/agent/skills/recovery/SKILL.md"},
	} {
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

func diagnosisAgentNames() []string {
	return []string{
		PlannerAgentName,
		MetricWorkerName,
		LogWorkerName,
		TraceWorkerName,
		TopologyWorkerName,
		DiagnosisAgentName,
		AlternativeAgentName,
		CriticAgentName,
		CognitiveRuntimeName,
	}
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

func (r *AgentRegistry) runConstrainedAgents(ctx context.Context, state *WorkflowState, deps constrainedToolDeps) error {
	if state == nil || state.Incident == nil {
		return fmt.Errorf("workflow state and Incident are required")
	}
	state.Incident.DiagnosisLedger = &state.DiagnosisLedger
	budgetState := state.Incident.AgentBudget
	budgets := safety.NewBudgetController(budgetState, r.limits, r.toolCosts)
	runtime := &constrainedRuntime{state: state, budgets: budgets, done: map[string]bool{}, transition: deps.Transition}
	runtime.hypotheses = safety.NewHypothesisTransitionService(&state.DiagnosisLedger, state.VerifiedHypotheses)
	defer func() { state.Incident.AgentBudget = budgets.State() }()
	var usageMu sync.Mutex
	var modelUsage []domain.ModelUsageEvent
	recordUsage := func(agentName string) func(*schema.Message, time.Duration) {
		return func(message *schema.Message, duration time.Duration) {
			usage := r.modelUsage(state.Incident.ID, agentName, message, duration)
			if agentName == SupervisorAgentName {
				usage.ParentAgent = ""
			}
			if agentName == DiagnosisAgentName {
				usage.ParentAgent = SupervisorAgentName
			}
			if state.Incident.Status == domain.StatusProposing || state.Incident.Status == domain.StatusAwaitingApproval {
				usage.Phase = "recovery"
			}
			usageMu.Lock()
			modelUsage = append(modelUsage, usage)
			usageMu.Unlock()
		}
	}
	defer func() {
		usageMu.Lock()
		captured := append([]domain.ModelUsageEvent(nil), modelUsage...)
		usageMu.Unlock()
		if len(captured) == 0 {
			return
		}
		if state.Incident.Investigation == nil {
			state.Incident.Investigation = &domain.Investigation{Architecture: "constrained-react", StartedAt: time.Now().UTC()}
		}
		state.Incident.Investigation.ModelUsage = append(state.Incident.Investigation.ModelUsage, captured...)
	}()
	ctx = withConstrainedRuntime(ctx, runtime)
	capabilityRegistry := captools.NewRegistry()
	diagnosisCapabilities, err := buildConstrainedDiagnosisCapabilities(deps)
	if err != nil {
		return err
	}
	recoveryCapabilities, err := buildConstrainedRecoveryCapabilities(deps)
	if err != nil {
		return err
	}
	if err = capabilityRegistry.RegisterAll(ctx, append(diagnosisCapabilities, recoveryCapabilities...)...); err != nil {
		return err
	}
	diagnosisToolsConfig, err := capabilityRegistry.ToolsNodeConfig(captools.NodeDiagnosisReact, false)
	if err != nil {
		return err
	}
	recoveryToolsConfig, err := capabilityRegistry.ToolsNodeConfig(captools.NodeRecoveryReact, true)
	if err != nil {
		return err
	}

	diagnosisMiddleware := newConstrainedAgentMiddleware(DiagnosisAgentName, r.skills[DiagnosisAgentName], "submit_diagnosis", "escalate_diagnosis").withUsageRecorder(recordUsage(DiagnosisAgentName))
	diagnosisAgent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{Name: DiagnosisAgentName, Description: "Investigate one Incident through evidence-driven hypothesis verification.", Instruction: "Operate as a bounded specialist. Use only the injected Skill and server-owned tool observations; finish with a structured terminal capability.", Model: r.chat, MaxIterations: r.limits[DiagnosisAgentName].MaxIterations, ModelRetryConfig: r.modelRetryConfig(), ToolsConfig: adk.ToolsConfig{ToolsNodeConfig: diagnosisToolsConfig, EmitInternalEvents: true}, Handlers: []adk.ChatModelAgentMiddleware{diagnosisMiddleware}})
	if err != nil {
		return err
	}
	recoveryMiddleware := newConstrainedAgentMiddleware(RecoveryAgentName, r.skills[RecoveryAgentName], "accept_recovery_proposal", "escalate_recovery").withUsageRecorder(recordUsage(RecoveryAgentName))
	recoveryAgent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{Name: RecoveryAgentName, Description: "Prepare and dry-run one constrained recovery proposal without mutation authority.", Instruction: "Operate only inside the proposal boundary defined by the injected Skill.", Model: r.chat, MaxIterations: r.limits[RecoveryAgentName].MaxIterations, ModelRetryConfig: r.modelRetryConfig(), ToolsConfig: adk.ToolsConfig{ToolsNodeConfig: recoveryToolsConfig, EmitInternalEvents: true}, Handlers: []adk.ChatModelAgentMiddleware{recoveryMiddleware}})
	if err != nil {
		return err
	}

	diagnosisAgentCapability, err := captools.WrapCapability(adk.NewAgentTool(ctx, diagnosisAgent), constrainedRegistration(captools.CategoryAgent, captools.NodeSupervisorReact))
	if err != nil {
		return err
	}
	recoveryAgentCapability, err := captools.WrapCapability(adk.NewAgentTool(ctx, recoveryAgent), constrainedRegistration(captools.CategoryAgent, captools.NodeSupervisorReact))
	if err != nil {
		return err
	}
	supervisorCapabilities, err := buildSupervisorTerminalCapabilities()
	if err != nil {
		return err
	}
	if err = capabilityRegistry.RegisterAll(ctx, append([]captools.Capability{diagnosisAgentCapability, recoveryAgentCapability}, supervisorCapabilities...)...); err != nil {
		return err
	}
	supervisorToolsConfig, err := capabilityRegistry.ToolsNodeConfig(captools.NodeSupervisorReact, true)
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
	supervisorMiddleware := newConstrainedAgentMiddleware(SupervisorAgentName, r.skills[SupervisorAgentName], "submit_supervisor_outcome", "escalate_incident").withToolFilter(supervisorFilter).withUsageRecorder(recordUsage(SupervisorAgentName))
	supervisorAgent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{Name: SupervisorAgentName, Description: "Coordinate diagnosis and recovery specialists for one Kubernetes Incident.", Instruction: "Act as the bounded incident commander defined by the injected Skill. Specialist decisions must be delegated through AgentTools.", Model: r.chat, MaxIterations: r.limits[SupervisorAgentName].MaxIterations, ModelRetryConfig: r.modelRetryConfig(), ToolsConfig: adk.ToolsConfig{ToolsNodeConfig: supervisorToolsConfig, EmitInternalEvents: true}, Handlers: []adk.ChatModelAgentMiddleware{supervisorMiddleware}})
	if err != nil {
		return err
	}

	payload, _ := json.Marshal(map[string]any{"incident": safeIncident(state.Incident), "workflow": WorkflowName, "objective": "coordinate an evidence-grounded diagnosis and, only after acceptance, a dry-run validated recovery proposal"})
	runner := adk.NewRunner(ctx, adk.RunnerConfig{Agent: supervisorAgent, EnableStreaming: true})
	options := make([]adk.AgentRunOption, 0, 2)
	if r.requestMaxTokens > 0 {
		options = append(options, adk.WithChatModelOptions([]model.Option{model.WithMaxTokens(r.requestMaxTokens)}))
		options = append(options, adk.WithAgentToolRunOptions(map[string][]adk.AgentRunOption{
			DiagnosisAgentName: {adk.WithChatModelOptions([]model.Option{model.WithMaxTokens(r.requestMaxTokens)})},
			RecoveryAgentName:  {adk.WithChatModelOptions([]model.Option{model.WithMaxTokens(r.requestMaxTokens)})},
		}))
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
			handled, handleErr := handleAgentBudgetExhaustion(ctx, runtime, event.Err)
			if handleErr != nil {
				return handleErr
			}
			if handled {
				state.Incident.AgentBudget = budgets.State()
				return nil
			}
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

func handleAgentBudgetExhaustion(ctx context.Context, runtime *constrainedRuntime, runErr error) (bool, error) {
	var exhausted safety.ErrBudgetExceeded
	if !errors.As(runErr, &exhausted) {
		return false, nil
	}
	agentName := exhausted.Agent
	if agentName == "" {
		agentName = SupervisorAgentName
	}
	feedback := safety.HumanRequired(safetyScopeForAgent(agentName), "agent_budget_exhausted", "the Agent reached its independent execution budget and requires human attention")
	runtime.mu.Lock()
	runtime.state.DiagnosisLedger.SafetyFeedback = append(runtime.state.DiagnosisLedger.SafetyFeedback, feedback)
	runtime.markDoneLocked(agentName)
	runtime.markDoneLocked(SupervisorAgentName)
	alreadyNeedsAttention := runtime.state.Incident.Status == domain.StatusNeedsAttention
	runtime.mu.Unlock()
	if !alreadyNeedsAttention {
		if err := runtime.transitionIncident(ctx, domain.StatusNeedsAttention); err != nil {
			return true, err
		}
	}
	return true, nil
}

type supervisorOutcome struct {
	Status string `json:"status"`
	Reason string `json:"reason"`
}

func buildSupervisorTerminalCapabilities() ([]captools.Capability, error) {
	submit, err := captools.NewCapability("submit_supervisor_outcome", "Submit the current server-validated diagnosis and proposal outcome.", func(ctx context.Context, in supervisorOutcome) (constrainedToolOutput, error) {
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
	}, constrainedRegistration(captools.CategoryDecision, captools.NodeSupervisorReact))
	if err != nil {
		return nil, err
	}
	escalate, err := captools.NewCapability("escalate_incident", "Request human attention without changing recovery authority.", func(ctx context.Context, in supervisorOutcome) (constrainedToolOutput, error) {
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
	}, constrainedRegistration(captools.CategoryDecision, captools.NodeSupervisorReact))
	if err != nil {
		return nil, err
	}
	return []captools.Capability{submit, escalate}, nil
}

func cloneToolCosts(in map[string]int) map[string]int {
	out := make(map[string]int, len(in))
	for name, cost := range in {
		out[name] = cost
	}
	return out
}

func (r *AgentRegistry) modelRetryConfig() *adk.ModelRetryConfig {
	maxRetries := r.modelMaxRetries
	if maxRetries <= 0 {
		maxRetries = 3
	}
	if maxRetries > 3 {
		maxRetries = 3
	}
	return &adk.ModelRetryConfig{
		MaxRetries: maxRetries,
		IsRetryAble: func(ctx context.Context, err error) bool {
			// The request-scoped model timeout is a child context. The Agent
			// context remains live here, so a timed-out stream can be replayed.
			return err != nil && ctx.Err() == nil
		},
		BackoffFunc: func(_ context.Context, attempt int) time.Duration {
			if attempt < 1 {
				attempt = 1
			}
			return time.Duration(attempt) * 250 * time.Millisecond
		},
	}
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
