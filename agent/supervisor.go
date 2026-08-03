package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/cloudwego/eino/components/tool"
	toolutils "github.com/cloudwego/eino/components/tool/utils"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	workflowgraph "github.com/kubepilot-aiops/kubepilot/graph"
	"github.com/kubepilot-aiops/kubepilot/internal/domain"
	captools "github.com/kubepilot-aiops/kubepilot/tools"
	"github.com/oklog/ulid/v2"
)

type Supervisor struct {
	runnable    compose.Runnable[*WorkflowState, *WorkflowState]
	checkpoints interface {
		Delete(context.Context, string) error
	}
	eventSink workflowgraph.EventSink
	hooks     *supervisorHooks
}

type supervisorHooks struct{ eventSink workflowgraph.EventSink }

func (s *Supervisor) SetEventSink(sink workflowgraph.EventSink) {
	s.eventSink = sink
	if s.hooks != nil {
		s.hooks.eventSink = sink
	}
}

type SupervisorDeps struct {
	Collectors  map[string]Collector
	Historical  Collector
	Agents      *AgentRegistry
	Executor    Executor
	Checkpoints compose.CheckPointStore
}

type evidenceToolInput struct {
	Incident domain.Incident `json:"incident"`
}
type evidenceToolResult struct {
	Source   string            `json:"source"`
	Evidence []domain.Evidence `json:"evidence,omitempty"`
	Error    string            `json:"error,omitempty"`
}
type recoveryToolInput struct {
	Incident         domain.Incident          `json:"incident"`
	ExecutionContext *domain.ExecutionContext `json:"execution_context,omitempty"`
}
type recoveryToolResult struct {
	DryRun       *domain.DryRunResult     `json:"dry_run,omitempty"`
	Proposal     *domain.RecoveryProposal `json:"proposal,omitempty"`
	Verification *domain.Verification     `json:"verification,omitempty"`
	Probe        *verificationProbeResult `json:"probe,omitempty"`
	Executed     bool                     `json:"executed,omitempty"`
	Unknown      bool                     `json:"unknown,omitempty"`
	Error        string                   `json:"error,omitempty"`
}

type verificationProbeResult struct {
	Name       string          `json:"name"`
	Applicable bool            `json:"applicable"`
	Success    bool            `json:"success"`
	Checks     map[string]bool `json:"checks,omitempty"`
	Message    string          `json:"message,omitempty"`
}

type approvalInterruptState struct{ State *WorkflowState }
type ApprovalResumeData struct {
	Approved bool                    `json:"approved"`
	Context  domain.ExecutionContext `json:"execution_context"`
}

func init() {
	schema.RegisterName[*WorkflowState]("kubepilot_incident_state_v2")
	schema.RegisterName[*approvalInterruptState]("kubepilot_approval_interrupt_v2")
	schema.RegisterName[*ApprovalResumeData]("kubepilot_approval_resume_v2")
}

func NewSupervisor(ctx context.Context, deps SupervisorDeps) (*Supervisor, error) {
	hooks := &supervisorHooks{}
	transition := func(ctx context.Context, incident *domain.Incident, to domain.IncidentStatus) error {
		from := incident.Status
		if err := domain.Transition(incident, to); err != nil {
			return err
		}
		if hooks.eventSink != nil && from != to {
			hooks.eventSink(ctx, workflowgraph.WorkflowEvent{IncidentID: incident.ID, RunID: ulid.Make().String(), Type: "status_transition", Name: string(to), Component: "EinoGraph", OccurredAt: time.Now().UTC()})
		}
		return nil
	}
	if deps.Agents == nil {
		return nil, fmt.Errorf("Eino ADK agent registry is required")
	}
	evidenceTools, err := buildEvidenceTools(deps.Collectors)
	if err != nil {
		return nil, err
	}
	historyTools, err := buildHistoricalTools(deps.Historical)
	if err != nil {
		return nil, err
	}
	dryRunTools, actionTools, verificationTools, err := buildRecoveryTools(deps.Executor, deps.Collectors)
	if err != nil {
		return nil, err
	}
	capabilityRegistry := captools.NewRegistry()
	register := func(items []tool.BaseTool, meta captools.Registration) error {
		for _, item := range items {
			if registerErr := capabilityRegistry.Register(ctx, item, meta); registerErr != nil {
				return registerErr
			}
		}
		return nil
	}
	if err = register(evidenceTools, captools.Registration{Category: captools.CategoryObservability, AllowedNodes: []string{"evidence_tools_node"}, Timeout: 30 * time.Second, MaxArgumentBytes: 256 << 10, MaxOutputBytes: 2 << 20}); err != nil {
		return nil, err
	}
	if err = register(historyTools, captools.Registration{Category: captools.CategoryIncident, AllowedNodes: []string{"historical_retrieval_tool"}, Timeout: 30 * time.Second, MaxArgumentBytes: 256 << 10, MaxOutputBytes: 2 << 20}); err != nil {
		return nil, err
	}
	if err = register(dryRunTools, captools.Registration{Category: captools.CategoryDryRun, AllowedNodes: []string{"recovery_dry_run"}, Timeout: 30 * time.Second, MaxArgumentBytes: 256 << 10, MaxOutputBytes: 2 << 20}); err != nil {
		return nil, err
	}
	if err = register(actionTools, captools.Registration{Category: captools.CategoryAction, AllowedNodes: []string{"action_tools_node"}, Timeout: 30 * time.Second, MaxArgumentBytes: 256 << 10, MaxOutputBytes: 2 << 20, ApprovalMiddleware: true}); err != nil {
		return nil, err
	}
	if err = register(verificationTools, captools.Registration{Category: captools.CategoryVerification, AllowedNodes: []string{"verification_tools_node"}, Timeout: 125 * time.Second, MaxArgumentBytes: 256 << 10, MaxOutputBytes: 2 << 20}); err != nil {
		return nil, err
	}
	evidenceTools, _ = capabilityRegistry.ToolsForNode("evidence_tools_node")
	historyTools, _ = capabilityRegistry.ToolsForNode("historical_retrieval_tool")
	dryRunTools, _ = capabilityRegistry.ToolsForNode("recovery_dry_run")
	actionTools, _ = capabilityRegistry.ToolsForNode("action_tools_node")
	verificationTools, _ = capabilityRegistry.ToolsForNode("verification_tools_node")
	evidenceNode, err := compose.NewToolNode(ctx, &compose.ToolsNodeConfig{Tools: evidenceTools, ExecuteSequentially: false})
	if err != nil {
		return nil, err
	}
	historyNode, err := compose.NewToolNode(ctx, &compose.ToolsNodeConfig{Tools: historyTools, ExecuteSequentially: true})
	if err != nil {
		return nil, err
	}
	dryRunNode, err := compose.NewToolNode(ctx, &compose.ToolsNodeConfig{Tools: dryRunTools, ExecuteSequentially: true})
	if err != nil {
		return nil, err
	}
	actionNode, err := compose.NewToolNode(ctx, &compose.ToolsNodeConfig{Tools: actionTools, ExecuteSequentially: true})
	if err != nil {
		return nil, err
	}
	verificationNode, err := compose.NewToolNode(ctx, &compose.ToolsNodeConfig{Tools: verificationTools, ExecuteSequentially: false})
	if err != nil {
		return nil, err
	}

	g := compose.NewGraph[*WorkflowState, *WorkflowState](compose.WithGenLocalState(func(context.Context) *WorkflowState { return &WorkflowState{Version: WorkflowVersion} }))
	add := func(name string, fn any) error {
		switch f := fn.(type) {
		case func(context.Context, *WorkflowState) (*WorkflowState, error):
			return g.AddLambdaNode(name, compose.InvokableLambda(f), compose.WithNodeName(name))
		case func(context.Context, *WorkflowState) (*schema.Message, error):
			return g.AddLambdaNode(name, compose.InvokableLambda(f), compose.WithNodeName(name))
		case func(context.Context, []*schema.Message) (*WorkflowState, error):
			return g.AddLambdaNode(name, compose.InvokableLambda(f), compose.WithNodeName(name))
		default:
			return fmt.Errorf("unsupported node %s", name)
		}
	}
	if err = add("incident_intake", func(ctx context.Context, in *WorkflowState) (*WorkflowState, error) {
		in.Version = WorkflowVersion
		if err := transition(ctx, in.Incident, domain.StatusCorrelating); err != nil {
			return in, err
		}
		return in, compose.ProcessState[*WorkflowState](ctx, func(_ context.Context, s *WorkflowState) error { *s = *in; return nil })
	}); err != nil {
		return nil, err
	}
	if err = add("alert_correlation", statusNode(transition, domain.StatusCollecting)); err != nil {
		return nil, err
	}
	if err = add("supervisor_agent", func(ctx context.Context, s *WorkflowState) (*WorkflowState, error) {
		windowEnd := time.Now().UTC()
		windowStart := s.Incident.EvidenceStartAt
		if windowStart.IsZero() {
			windowStart = windowEnd.Add(-5 * time.Minute)
		}
		payload, _ := json.Marshal(map[string]any{"task": "evidence_plan", "incident": safeIncident(s.Incident), "required_sources": []string{"metric", "log", "trace", "kubernetes"}, "window_start": windowStart, "window_end": windowEnd})
		var plan EvidencePlan
		if err := deps.Agents.Run(ctx, SupervisorAgentName, string(payload), &plan); err != nil {
			return s, err
		}
		// The model may select structured filters, but only the server controls
		// the authoritative Incident time window.
		plan.WindowStart = windowStart
		plan.WindowEnd = windowEnd
		if err := validateEvidencePlan(plan); err != nil {
			return s, err
		}
		s.EvidencePlan = plan
		return s, compose.ProcessState[*WorkflowState](ctx, func(_ context.Context, local *WorkflowState) error {
			local.EvidencePlan = plan
			return nil
		})
	}); err != nil {
		return nil, err
	}
	if err = add("evidence_plan_validator", func(_ context.Context, s *WorkflowState) (*schema.Message, error) {
		calls := make([]schema.ToolCall, 0, 4)
		for _, source := range []string{"metric", "log", "trace", "kubernetes"} {
			args, _ := json.Marshal(evidenceToolInput{Incident: *s.Incident})
			calls = append(calls, schema.ToolCall{ID: "evidence-" + source + "-" + ulid.Make().String(), Type: "function", Function: schema.FunctionCall{Name: evidenceToolName(source), Arguments: string(args)}})
		}
		return &schema.Message{Role: schema.Assistant, ToolCalls: calls}, nil
	}); err != nil {
		return nil, err
	}
	if err = g.AddToolsNode("evidence_tools_node", evidenceNode, compose.WithNodeName("evidence_tools_node")); err != nil {
		return nil, err
	}
	if err = add("evidence_fusion", func(ctx context.Context, messages []*schema.Message) (*WorkflowState, error) {
		var current *WorkflowState
		err := compose.ProcessState[*WorkflowState](ctx, func(_ context.Context, s *WorkflowState) error {
			current = s
			return mergeEvidenceToolMessages(s, messages)
		})
		if err != nil {
			return nil, err
		}
		if err = transition(ctx, current.Incident, domain.StatusDiagnosing); err != nil {
			return nil, err
		}
		return current, nil
	}); err != nil {
		return nil, err
	}
	if err = add("historical_retrieval_plan", func(_ context.Context, s *WorkflowState) (*schema.Message, error) {
		args, _ := json.Marshal(evidenceToolInput{Incident: *s.Incident})
		return &schema.Message{Role: schema.Assistant, ToolCalls: []schema.ToolCall{{ID: "history-" + ulid.Make().String(), Type: "function", Function: schema.FunctionCall{Name: "retrieve_historical_incidents", Arguments: string(args)}}}}, nil
	}); err != nil {
		return nil, err
	}
	if err = g.AddToolsNode("historical_retrieval_tool", historyNode, compose.WithNodeName("historical_retrieval_tool")); err != nil {
		return nil, err
	}
	if err = add("historical_merger", func(ctx context.Context, messages []*schema.Message) (*WorkflowState, error) {
		var current *WorkflowState
		err := compose.ProcessState[*WorkflowState](ctx, func(_ context.Context, s *WorkflowState) error {
			current = s
			return mergeEvidenceToolMessages(s, messages)
		})
		return current, err
	}); err != nil {
		return nil, err
	}
	if err = add("diagnosis_agent", func(ctx context.Context, s *WorkflowState) (*WorkflowState, error) {
		payload, _ := json.Marshal(map[string]any{"incident": safeIncident(s.Incident), "evidence": compactEvidence(s.Incident.Evidence)})
		var decision DiagnosisDecision
		if err := deps.Agents.Run(ctx, DiagnosisAgentName, string(payload), &decision); err != nil {
			return s, err
		}
		if err := applyDiagnosisDecision(s.Incident, decision); err != nil {
			return s, err
		}
		s.DiagnosisAttempts++
		if decision.RequestAdditionalEvidence && s.DiagnosisAttempts == 1 {
			if err := transition(ctx, s.Incident, domain.StatusCollecting); err != nil {
				return s, err
			}
		} else if decision.Confidence < .8 {
			if err := transition(ctx, s.Incident, domain.StatusNeedsAttention); err != nil {
				return s, err
			}
		} else {
			if err := transition(ctx, s.Incident, domain.StatusProposing); err != nil {
				return s, err
			}
		}
		return s, compose.ProcessState[*WorkflowState](ctx, func(_ context.Context, local *WorkflowState) error {
			local.DiagnosisAttempts = s.DiagnosisAttempts
			local.Incident = s.Incident
			return nil
		})
	}); err != nil {
		return nil, err
	}
	if err = add("recovery_agent", func(ctx context.Context, s *WorkflowState) (*WorkflowState, error) {
		payload, _ := json.Marshal(map[string]any{"root_cause": s.Incident.RootCause, "category": s.Incident.RootCauseCategory, "service": s.Incident.RootCauseService, "resource": s.Incident.RootCauseResource, "evidence_ids": s.Incident.RootCauseEvidenceIDs})
		var decision RecoveryDecision
		if err := deps.Agents.Run(ctx, RecoveryAgentName, string(payload), &decision); err != nil {
			return s, err
		}
		proposal, err := recoveryProposal(s.Incident, decision)
		if err != nil {
			return s, err
		}
		s.Incident.Proposal = proposal
		return s, nil
	}); err != nil {
		return nil, err
	}
	if err = add("proposal_validator", func(ctx context.Context, s *WorkflowState) (*WorkflowState, error) {
		if err := validateRecoveryProposal(s.Incident); err != nil {
			_ = transition(ctx, s.Incident, domain.StatusNeedsAttention)
			s.Errors = append(s.Errors, err.Error())
		}
		return s, nil
	}); err != nil {
		return nil, err
	}
	if err = add("recovery_dry_run_plan", func(_ context.Context, s *WorkflowState) (*schema.Message, error) {
		args, _ := json.Marshal(recoveryToolInput{Incident: *s.Incident})
		return &schema.Message{Role: schema.Assistant, ToolCalls: []schema.ToolCall{{ID: "dry-run-" + ulid.Make().String(), Type: "function", Function: schema.FunctionCall{Name: "dry_run_" + recoveryToolName(s.Incident.Proposal.Action), Arguments: string(args)}}}}, nil
	}); err != nil {
		return nil, err
	}
	if err = g.AddToolsNode("recovery_dry_run", dryRunNode, compose.WithNodeName("recovery_dry_run")); err != nil {
		return nil, err
	}
	if err = add("recovery_dry_run_merger", func(ctx context.Context, messages []*schema.Message) (*WorkflowState, error) {
		var s *WorkflowState
		err := compose.ProcessState[*WorkflowState](ctx, func(_ context.Context, state *WorkflowState) error {
			s = state
			var result recoveryToolResult
			if len(messages) != 1 {
				return fmt.Errorf("dry-run returned %d results", len(messages))
			}
			if e := json.Unmarshal([]byte(messages[0].Content), &result); e != nil {
				return e
			}
			state.ToolCalls++
			state.DryRun = result.DryRun
			state.Incident.DryRun = result.DryRun
			if result.Proposal != nil {
				state.Incident.Proposal = result.Proposal
			}
			if result.Error != "" || result.DryRun == nil || !result.DryRun.Success {
				if e := transition(ctx, state.Incident, domain.StatusNeedsAttention); e != nil {
					return e
				}
				state.Errors = append(state.Errors, result.Error)
			}
			return nil
		})
		return s, err
	}); err != nil {
		return nil, err
	}
	if err = add("approval_interrupt", func(ctx context.Context, s *WorkflowState) (*WorkflowState, error) {
		return approvalNode(ctx, s, transition)
	}); err != nil {
		return nil, err
	}
	if err = add("action_plan", func(_ context.Context, s *WorkflowState) (*schema.Message, error) {
		if err := validateExecutionContext(s); err != nil {
			return nil, err
		}
		args, _ := json.Marshal(recoveryToolInput{Incident: *s.Incident, ExecutionContext: s.ExecutionContext})
		return &schema.Message{Role: schema.Assistant, ToolCalls: []schema.ToolCall{{ID: "action-" + ulid.Make().String(), Type: "function", Function: schema.FunctionCall{Name: recoveryToolName(s.Incident.Proposal.Action), Arguments: string(args)}}}}, nil
	}); err != nil {
		return nil, err
	}
	if err = g.AddToolsNode("action_tools_node", actionNode, compose.WithNodeName("action_tools_node")); err != nil {
		return nil, err
	}
	if err = add("action_result", func(ctx context.Context, messages []*schema.Message) (*WorkflowState, error) {
		var s *WorkflowState
		err := compose.ProcessState[*WorkflowState](ctx, func(_ context.Context, state *WorkflowState) error {
			s = state
			var result recoveryToolResult
			if len(messages) != 1 {
				return fmt.Errorf("action returned %d results", len(messages))
			}
			if e := json.Unmarshal([]byte(messages[0].Content), &result); e != nil {
				return e
			}
			state.ToolCalls++
			if result.Error != "" || !result.Executed {
				targetStatus := domain.StatusRecoveryFailed
				if result.Unknown {
					targetStatus = domain.StatusNeedsAttention
				}
				if e := transition(ctx, state.Incident, targetStatus); e != nil {
					return e
				}
				state.Errors = append(state.Errors, result.Error)
			} else {
				if e := transition(ctx, state.Incident, domain.StatusVerifying); e != nil {
					return e
				}
				state.VerificationState.StartedAt = time.Now().UTC()
			}
			return nil
		})
		return s, err
	}); err != nil {
		return nil, err
	}
	if err = add("verification_plan", func(_ context.Context, s *WorkflowState) (*schema.Message, error) {
		args, _ := json.Marshal(recoveryToolInput{Incident: *s.Incident, ExecutionContext: s.ExecutionContext})
		calls := make([]schema.ToolCall, 0, 5)
		for _, name := range []string{"verify_kubernetes_health", "verify_prometheus_recovery", "verify_loki_recovery", "verify_trace_recovery", "verify_business_probe"} {
			calls = append(calls, schema.ToolCall{ID: name + "-" + ulid.Make().String(), Type: "function", Function: schema.FunctionCall{Name: name, Arguments: string(args)}})
		}
		return &schema.Message{Role: schema.Assistant, ToolCalls: calls}, nil
	}); err != nil {
		return nil, err
	}
	if err = g.AddToolsNode("verification_tools_node", verificationNode, compose.WithNodeName("verification_tools_node")); err != nil {
		return nil, err
	}
	if err = add("verification_agent", func(ctx context.Context, messages []*schema.Message) (*WorkflowState, error) {
		var s *WorkflowState
		err := compose.ProcessState[*WorkflowState](ctx, func(_ context.Context, state *WorkflowState) error {
			s = state
			if len(messages) != 5 {
				return fmt.Errorf("verification returned %d results", len(messages))
			}
			state.VerificationState.Attempts++
			combined := domain.Verification{Success: true, Checks: map[string]bool{}, CompletedAt: time.Now().UTC()}
			var failures []string
			for _, message := range messages {
				var result recoveryToolResult
				if e := json.Unmarshal([]byte(message.Content), &result); e != nil {
					return e
				}
				state.ToolCalls++
				if result.Error != "" {
					combined.Success = false
					failures = append(failures, result.Error)
					continue
				}
				if result.Verification != nil {
					combined.Success = combined.Success && result.Verification.Success
					for name, passed := range result.Verification.Checks {
						combined.Checks[name] = passed
					}
					if !result.Verification.Success {
						failures = append(failures, result.Verification.Message)
					}
				}
				if result.Probe != nil {
					combined.Checks[result.Probe.Name+"_applicable"] = result.Probe.Applicable
					if result.Probe.Applicable {
						combined.Checks[result.Probe.Name] = result.Probe.Success
						combined.Success = combined.Success && result.Probe.Success
						if !result.Probe.Success {
							failures = append(failures, result.Probe.Message)
						}
					}
				}
			}
			combined.Message = "all applicable recovery checks passed"
			if len(failures) > 0 {
				combined.Message = strings.Join(failures, "; ")
			}
			state.Incident.Verification = &combined
			if len(failures) > 0 && len(combined.Checks) == 0 {
				if e := transition(ctx, state.Incident, domain.StatusRecoveryFailed); e != nil {
					return e
				}
				state.Errors = append(state.Errors, failures...)
				return nil
			}
			if combined.Success {
				state.VerificationState.ConsecutiveSuccess = 3
				if e := transition(ctx, state.Incident, domain.StatusResolved); e != nil {
					return e
				}
			} else {
				if e := transition(ctx, state.Incident, domain.StatusRecoveryFailed); e != nil {
					return e
				}
			}
			return nil
		})
		return s, err
	}); err != nil {
		return nil, err
	}
	if err = add("incident_finalizer", func(_ context.Context, s *WorkflowState) (*WorkflowState, error) {
		s.Incident.UpdatedAt = time.Now().UTC()
		return s, nil
	}); err != nil {
		return nil, err
	}

	edges := [][2]string{{compose.START, "incident_intake"}, {"incident_intake", "alert_correlation"}, {"alert_correlation", "supervisor_agent"}, {"supervisor_agent", "evidence_plan_validator"}, {"evidence_plan_validator", "evidence_tools_node"}, {"evidence_tools_node", "evidence_fusion"}, {"evidence_fusion", "historical_retrieval_plan"}, {"historical_retrieval_plan", "historical_retrieval_tool"}, {"historical_retrieval_tool", "historical_merger"}, {"historical_merger", "diagnosis_agent"}}
	for _, edge := range edges {
		if err = g.AddEdge(edge[0], edge[1]); err != nil {
			return nil, err
		}
	}
	if err = g.AddBranch("diagnosis_agent", compose.NewGraphBranch(func(_ context.Context, s *WorkflowState) (string, error) {
		if s.Incident.Status == domain.StatusCollecting {
			return "evidence_plan_validator", nil
		}
		if s.Incident.Status == domain.StatusNeedsAttention {
			return "incident_finalizer", nil
		}
		return "recovery_agent", nil
	}, map[string]bool{"evidence_plan_validator": true, "incident_finalizer": true, "recovery_agent": true})); err != nil {
		return nil, err
	}
	if err = g.AddEdge("recovery_agent", "proposal_validator"); err != nil {
		return nil, err
	}
	if err = g.AddBranch("proposal_validator", compose.NewGraphBranch(func(_ context.Context, s *WorkflowState) (string, error) {
		if s.Incident.Status == domain.StatusNeedsAttention {
			return "incident_finalizer", nil
		}
		return "recovery_dry_run_plan", nil
	}, map[string]bool{"incident_finalizer": true, "recovery_dry_run_plan": true})); err != nil {
		return nil, err
	}
	if err = g.AddEdge("recovery_dry_run_plan", "recovery_dry_run"); err != nil {
		return nil, err
	}
	if err = g.AddEdge("recovery_dry_run", "recovery_dry_run_merger"); err != nil {
		return nil, err
	}
	if err = g.AddBranch("recovery_dry_run_merger", compose.NewGraphBranch(func(_ context.Context, s *WorkflowState) (string, error) {
		if s.Incident.Status == domain.StatusNeedsAttention {
			return "incident_finalizer", nil
		}
		return "approval_interrupt", nil
	}, map[string]bool{"incident_finalizer": true, "approval_interrupt": true})); err != nil {
		return nil, err
	}
	if err = g.AddBranch("approval_interrupt", compose.NewGraphBranch(func(_ context.Context, s *WorkflowState) (string, error) {
		if s.Incident.Status == domain.StatusRejected {
			return "incident_finalizer", nil
		}
		return "action_plan", nil
	}, map[string]bool{"incident_finalizer": true, "action_plan": true})); err != nil {
		return nil, err
	}
	for _, edge := range [][2]string{{"action_plan", "action_tools_node"}, {"action_tools_node", "action_result"}, {"verification_plan", "verification_tools_node"}, {"verification_tools_node", "verification_agent"}} {
		if err = g.AddEdge(edge[0], edge[1]); err != nil {
			return nil, err
		}
	}
	if err = g.AddBranch("action_result", compose.NewGraphBranch(func(_ context.Context, s *WorkflowState) (string, error) {
		if s.Incident.Status == domain.StatusRecoveryFailed || s.Incident.Status == domain.StatusNeedsAttention {
			return "incident_finalizer", nil
		}
		return "verification_plan", nil
	}, map[string]bool{"incident_finalizer": true, "verification_plan": true})); err != nil {
		return nil, err
	}
	if err = g.AddEdge("verification_agent", "incident_finalizer"); err != nil {
		return nil, err
	}
	if err = g.AddEdge("incident_finalizer", compose.END); err != nil {
		return nil, err
	}
	options := []compose.GraphCompileOption{compose.WithGraphName("kubepilot-incident-v2")}
	if deps.Checkpoints != nil {
		options = append(options, compose.WithCheckPointStore(deps.Checkpoints))
	}
	run, err := g.Compile(ctx, options...)
	if err != nil {
		return nil, err
	}
	var checkpointDeleter interface {
		Delete(context.Context, string) error
	}
	if candidate, ok := deps.Checkpoints.(interface {
		Delete(context.Context, string) error
	}); ok {
		checkpointDeleter = candidate
	}
	return &Supervisor{runnable: run, checkpoints: checkpointDeleter, hooks: hooks}, nil
}

func buildEvidenceTools(collectors map[string]Collector) ([]tool.BaseTool, error) {
	var out []tool.BaseTool
	for _, source := range []string{"metric", "log", "trace", "kubernetes"} {
		c := collectors[source]
		name := evidenceToolName(source)
		t, err := toolutils.InferTool(name, "Collect bounded, structured "+source+" evidence for one Incident.", func(ctx context.Context, in evidenceToolInput) (evidenceToolResult, error) {
			if c == nil {
				return evidenceToolResult{Source: source, Error: "collector unavailable"}, nil
			}
			ev, e := c.Collect(ctx, &in.Incident)
			if e != nil {
				return evidenceToolResult{Source: source, Error: e.Error()}, nil
			}
			return evidenceToolResult{Source: source, Evidence: ev}, nil
		})
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, nil
}
func buildHistoricalTools(c Collector) ([]tool.BaseTool, error) {
	t, err := toolutils.InferTool("retrieve_historical_incidents", "Retrieve historical incidents using only fused evidence.", func(ctx context.Context, in evidenceToolInput) (evidenceToolResult, error) {
		if c == nil {
			return evidenceToolResult{Source: "historical"}, nil
		}
		ev, e := c.Collect(ctx, &in.Incident)
		if e != nil {
			return evidenceToolResult{Source: "historical", Error: e.Error()}, nil
		}
		return evidenceToolResult{Source: "historical", Evidence: ev}, nil
	})
	if err != nil {
		return nil, err
	}
	return []tool.BaseTool{t}, nil
}

func buildRecoveryTools(executor Executor, collectors map[string]Collector) (dryRun, action, verification []tool.BaseTool, err error) {
	if executor == nil {
		return nil, nil, nil, fmt.Errorf("recovery executor is required")
	}
	for _, recoveryAction := range []domain.RecoveryAction{domain.ActionRestartPod, domain.ActionScaleDeployment, domain.ActionRollbackDeployment} {
		actionName := recoveryToolName(recoveryAction)
		dryTool, toolErr := toolutils.InferTool("dry_run_"+actionName, "Validate and Kubernetes dry-run one constrained recovery mutation.", func(ctx context.Context, in recoveryToolInput) (recoveryToolResult, error) {
			if in.Incident.Proposal == nil || in.Incident.Proposal.Action != recoveryAction {
				return recoveryToolResult{Error: "proposal action does not match tool"}, nil
			}
			result, dryErr := dryRunProposal(ctx, executor, &in.Incident)
			if dryErr != nil {
				return recoveryToolResult{DryRun: result, Proposal: in.Incident.Proposal, Error: dryErr.Error()}, nil
			}
			return recoveryToolResult{DryRun: result, Proposal: in.Incident.Proposal}, nil
		})
		if toolErr != nil {
			return nil, nil, nil, toolErr
		}
		dryRun = append(dryRun, dryTool)

		actionTool, toolErr := toolutils.InferTool(actionName, "Execute one approved and dry-run validated Kubernetes recovery mutation.", func(ctx context.Context, in recoveryToolInput) (recoveryToolResult, error) {
			if in.Incident.Proposal == nil || in.Incident.Proposal.Action != recoveryAction {
				return recoveryToolResult{Error: "proposal action does not match tool"}, nil
			}
			state := &WorkflowState{Incident: &in.Incident, DryRun: in.Incident.DryRun, ExecutionContext: in.ExecutionContext}
			if validateErr := validateExecutionContext(state); validateErr != nil {
				return recoveryToolResult{Error: validateErr.Error()}, nil
			}
			if executeErr := executor.Execute(ctx, &in.Incident, *in.Incident.Proposal); executeErr != nil {
				return recoveryToolResult{Error: executeErr.Error(), Unknown: errors.Is(executeErr, ErrActionResultUnknown)}, nil
			}
			return recoveryToolResult{Executed: true}, nil
		})
		if toolErr != nil {
			return nil, nil, nil, toolErr
		}
		action = append(action, actionTool)
	}
	verifyTool, toolErr := toolutils.InferTool("verify_kubernetes_health", "Verify Kubernetes recovery health until three consecutive samples pass or timeout.", func(ctx context.Context, in recoveryToolInput) (recoveryToolResult, error) {
		result, verifyErr := executor.Verify(ctx, &in.Incident)
		if verifyErr != nil {
			return recoveryToolResult{Error: verifyErr.Error()}, nil
		}
		return recoveryToolResult{Verification: &result}, nil
	})
	if toolErr != nil {
		return nil, nil, nil, toolErr
	}
	verification = append(verification, verifyTool)
	for _, source := range []string{"metric", "log", "trace", "business"} {
		source := source
		name := map[string]string{"metric": "verify_prometheus_recovery", "log": "verify_loki_recovery", "trace": "verify_trace_recovery", "business": "verify_business_probe"}[source]
		verifySource, sourceErr := toolutils.InferTool(name, "Run one bounded, structured recovery verification probe.", func(ctx context.Context, in recoveryToolInput) (recoveryToolResult, error) {
			collector := collectors[source]
			if collector == nil {
				return recoveryToolResult{Probe: &verificationProbeResult{Name: source, Applicable: false, Message: source + " verification is not configured"}}, nil
			}
			incident := in.Incident
			if in.ExecutionContext != nil && in.ExecutionContext.ApprovedAt.After(incident.EvidenceStartAt) {
				incident.EvidenceStartAt = in.ExecutionContext.ApprovedAt
			}
			evidence, collectErr := collector.Collect(ctx, &incident)
			if collectErr != nil {
				return recoveryToolResult{Error: name + ": " + collectErr.Error()}, nil
			}
			probe := evaluateVerificationEvidence(source, evidence)
			return recoveryToolResult{Probe: &probe}, nil
		})
		if sourceErr != nil {
			return nil, nil, nil, sourceErr
		}
		verification = append(verification, verifySource)
	}
	return dryRun, action, verification, nil
}

func evaluateVerificationEvidence(source string, evidence []domain.Evidence) verificationProbeResult {
	result := verificationProbeResult{Name: source, Checks: map[string]bool{}}
	switch source {
	case "metric":
		result.Applicable = len(evidence) > 0
		result.Success = result.Applicable
		result.Checks["telemetry_available"] = result.Success
		result.Message = "Prometheus recovery telemetry is unavailable"
	case "log":
		result.Applicable = true
		recentErrors := 0
		for _, item := range evidence {
			if item.Type == "log_entry" || item.Kind == "log_entry" {
				recentErrors++
			}
		}
		result.Success = recentErrors == 0
		result.Checks["recent_error_templates_absent"] = result.Success
		result.Message = fmt.Sprintf("%d recent error log entries remain", recentErrors)
	case "trace":
		result.Applicable = len(evidence) > 0
		result.Success = true
		for _, item := range evidence {
			if nonEmptyEvidenceValue(item.Content, "error_service") || nonEmptyEvidenceValue(item.Data, "error_service") {
				result.Success = false
				break
			}
		}
		result.Checks["error_spans_absent"] = result.Success
		result.Message = "new traces still contain error spans"
	case "business":
		result.Applicable = len(evidence) > 0
		result.Success = result.Applicable
		for _, item := range evidence {
			if success, ok := item.Content["success"].(bool); ok && !success {
				result.Success = false
			}
		}
		result.Checks["business_probe"] = result.Success
		result.Message = "business probe did not succeed"
	}
	return result
}

func nonEmptyEvidenceValue(values map[string]any, key string) bool {
	if values == nil {
		return false
	}
	value, exists := values[key]
	return exists && value != nil && strings.TrimSpace(fmt.Sprint(value)) != ""
}

func recoveryToolName(action domain.RecoveryAction) string {
	if action == domain.ActionRestartPod {
		return "restart_workload"
	}
	return string(action)
}
func evidenceToolName(source string) string {
	switch source {
	case "metric":
		return "query_prometheus_evidence"
	case "log":
		return "query_loki_evidence"
	case "trace":
		return "query_trace_evidence"
	default:
		return "query_kubernetes_evidence"
	}
}
func mergeEvidenceToolMessages(s *WorkflowState, messages []*schema.Message) error {
	// The authoritative window closes only after bounded collection finishes;
	// model latency must never make freshly collected evidence appear stale.
	if s.Incident.Status == domain.StatusCollecting {
		s.EvidencePlan.WindowEnd = time.Now().UTC()
	}
	successful := map[string]bool{}
	seen := map[string]bool{}
	for _, existing := range s.Incident.Evidence {
		seen[existing.ID] = true
	}
	for _, m := range messages {
		var r evidenceToolResult
		if err := json.Unmarshal([]byte(m.Content), &r); err != nil {
			return err
		}
		s.ToolCalls++
		if r.Error != "" {
			s.Errors = append(s.Errors, r.Source+": "+r.Error)
			continue
		}
		successful[r.Source] = true
		for _, e := range r.Evidence {
			switch r.Source {
			case "metric":
				e.Source = "prometheus"
			case "log":
				e.Source = "loki"
			case "trace":
				e.Source = "jaeger"
			case "kubernetes":
				e.Source = "kubernetes"
			case "historical":
				e.Source = "historical"
			}
			if e.WindowStart.IsZero() {
				e.WindowStart = s.EvidencePlan.WindowStart
			}
			if e.WindowEnd.IsZero() {
				e.WindowEnd = s.EvidencePlan.WindowEnd
			}
			normalizeEvidence(&e, s.Incident)
			if r.Source != "historical" && !evidenceInWindow(e, s.EvidencePlan.WindowStart, s.EvidencePlan.WindowEnd) {
				continue
			}
			if !seen[e.ID] {
				seen[e.ID] = true
				s.Incident.Evidence = append(s.Incident.Evidence, e)
			}
		}
	}
	if s.Incident.Status == domain.StatusCollecting {
		if !successful["kubernetes"] {
			return fmt.Errorf("kubernetes evidence unavailable")
		}
		hasKubernetesEvidence, hasTelemetryEvidence := false, false
		for _, item := range s.Incident.Evidence {
			switch item.Source {
			case "kubernetes":
				hasKubernetesEvidence = true
			case "prometheus", "loki", "jaeger":
				hasTelemetryEvidence = true
			}
		}
		if !hasKubernetesEvidence {
			return fmt.Errorf("kubernetes evidence returned no usable records in window %s..%s (records=%d)", s.EvidencePlan.WindowStart.Format(time.RFC3339Nano), s.EvidencePlan.WindowEnd.Format(time.RFC3339Nano), len(s.Incident.Evidence))
		}
		if (!successful["metric"] && !successful["log"] && !successful["trace"]) || !hasTelemetryEvidence {
			return fmt.Errorf("telemetry evidence unavailable")
		}
	}
	sort.SliceStable(s.Incident.Evidence, func(i, j int) bool { return s.Incident.Evidence[i].Timestamp.Before(s.Incident.Evidence[j].Timestamp) })
	return nil
}

func evidenceInWindow(e domain.Evidence, start, end time.Time) bool {
	if e.Timestamp.IsZero() || start.IsZero() || end.IsZero() {
		return false
	}
	return !e.Timestamp.Before(start) && !e.Timestamp.After(end.Add(2*time.Second))
}
func normalizeEvidence(e *domain.Evidence, in *domain.Incident) {
	if e.Type == "" {
		e.Type = e.Kind
	}
	if e.Kind == "" {
		e.Kind = e.Type
	}
	if e.Timestamp.IsZero() {
		e.Timestamp = e.ObservedAt
	}
	if e.ObservedAt.IsZero() {
		e.ObservedAt = e.Timestamp
	}
	if e.Content == nil {
		e.Content = e.Data
	}
	if e.Data == nil {
		e.Data = e.Content
	}
	if e.Namespace == "" {
		e.Namespace = in.Namespace
	}
	if e.Service == "" {
		e.Service = in.Service
	}
	if e.Resource == "" {
		e.Resource = in.Resource
	}
	if e.CollectedAt.IsZero() {
		e.CollectedAt = time.Now().UTC()
	}
	if e.Confidence == 0 {
		e.Confidence = 1
	}
	if e.Confidence < 0 || e.Confidence > 1 {
		e.Confidence = 1
	}
	raw, _ := json.Marshal(e.Content)
	h := sha256.Sum256(append([]byte(e.Source+e.Type+e.Resource+e.WindowStart.UTC().Format(time.RFC3339Nano)+e.WindowEnd.UTC().Format(time.RFC3339Nano)), raw...))
	e.ID = hex.EncodeToString(h[:])
}
func applyDiagnosisDecision(in *domain.Incident, d DiagnosisDecision) error {
	valid := map[string]bool{}
	for _, e := range in.Evidence {
		valid[e.ID] = true
	}
	for _, id := range d.EvidenceIDs {
		if !valid[id] {
			return fmt.Errorf("diagnosis referenced unknown evidence %q", id)
		}
	}
	if d.ReasoningType != "hypothesis_verification" || len(d.Hypotheses) == 0 || len(d.Hypotheses) > 3 {
		return fmt.Errorf("invalid hypothesis verification")
	}
	in.ReasoningType = d.ReasoningType
	in.RootCause = d.RootCause
	in.RootCauseCategory = d.Category
	in.RootCauseVariant = d.Variant
	in.RootCauseService = d.Service
	in.RootCauseResource = d.Resource
	in.Confidence = d.Confidence
	in.RootCauseEvidenceIDs = append([]string(nil), d.EvidenceIDs...)
	in.Hypotheses = d.Hypotheses
	return nil
}
func recoveryProposal(in *domain.Incident, d RecoveryDecision) (*domain.RecoveryProposal, error) {
	target := in.RootCauseResource
	if target == "" {
		target = in.Resource
	}
	canonical, err := canonicalProposalTarget(d.Target, in.Namespace, target)
	if err != nil {
		return nil, err
	}
	return &domain.RecoveryProposal{ID: ulid.Make().String(), Action: d.Action, Namespace: in.Namespace, Target: canonical, Parameters: d.Parameters, Reason: d.Reason, Risk: d.Risk, Diff: d.Diff, Rollback: d.Rollback, Confidence: d.Confidence, ExpiresAt: time.Now().UTC().Add(15 * time.Minute)}, nil
}

func validateRecoveryProposal(in *domain.Incident) error {
	if in == nil || in.Proposal == nil {
		return fmt.Errorf("recovery proposal is required")
	}
	proposal := in.Proposal
	switch proposal.Action {
	case domain.ActionRestartPod, domain.ActionRollbackDeployment:
	case domain.ActionScaleDeployment:
		if _, err := proposalReplicas(proposal.Parameters); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unsupported recovery action %q", proposal.Action)
	}
	if proposal.Namespace != in.Namespace || proposal.Target == "" || proposal.Reason == "" || proposal.Risk == "" || proposal.Diff == "" || proposal.Rollback == "" {
		return fmt.Errorf("proposal target, reason, risk, diff, and rollback are required")
	}
	return nil
}
func dryRunProposal(ctx context.Context, executor Executor, in *domain.Incident) (*domain.DryRunResult, error) {
	if in.Proposal == nil {
		return &domain.DryRunResult{Error: "proposal missing"}, fmt.Errorf("proposal missing")
	}
	if runner, ok := executor.(interface {
		DryRun(context.Context, *domain.RecoveryProposal) (*domain.DryRunResult, error)
	}); ok {
		return runner.DryRun(ctx, in.Proposal)
	}
	err := fmt.Errorf("Kubernetes DryRunAll capability is required")
	return &domain.DryRunResult{Action: in.Proposal.Action, Target: in.Proposal.Target, Error: err.Error(), ValidatedAt: time.Now().UTC()}, err
}
func approvalNode(ctx context.Context, s *WorkflowState, transition func(context.Context, *domain.Incident, domain.IncidentStatus) error) (*WorkflowState, error) {
	was, has, state := compose.GetInterruptState[*approvalInterruptState](ctx)
	if !was {
		if err := transition(ctx, s.Incident, domain.StatusAwaitingApproval); err != nil {
			return s, err
		}
		return s, compose.StatefulInterrupt(ctx, map[string]any{"incident_id": s.Incident.ID, "proposal_id": s.Incident.Proposal.ID, "expires_at": s.Incident.Proposal.ExpiresAt}, &approvalInterruptState{State: s})
	}
	if has && state != nil && state.State != nil {
		s = state.State
	}
	isResume, hasData, data := compose.GetResumeContext[*ApprovalResumeData](ctx)
	if !isResume || !hasData || data == nil {
		return s, fmt.Errorf("approval resume data is required")
	}
	if !data.Approved {
		if err := transition(ctx, s.Incident, domain.StatusRejected); err != nil {
			return s, err
		}
		return s, compose.ProcessState[*WorkflowState](ctx, func(_ context.Context, local *WorkflowState) error { *local = *s; return nil })
	}
	s.ExecutionContext = &data.Context
	s.Incident.ExecutionContext = &data.Context
	if err := transition(ctx, s.Incident, domain.StatusRecovering); err != nil {
		return s, err
	}
	return s, compose.ProcessState[*WorkflowState](ctx, func(_ context.Context, local *WorkflowState) error { *local = *s; return nil })
}
func validateExecutionContext(s *WorkflowState) error {
	if s.ExecutionContext == nil || s.Incident.Proposal == nil || s.DryRun == nil {
		return fmt.Errorf("execution context, proposal and dry-run are required")
	}
	e := s.ExecutionContext
	if e.IncidentID != s.Incident.ID || e.ProposalID != s.Incident.Proposal.ID || e.MutationSpecHash != s.DryRun.MutationSpecHash {
		return fmt.Errorf("execution context does not match approved dry-run")
	}
	if e.TargetUID != s.Incident.Proposal.TargetUID || e.ResourceVersion != s.Incident.Proposal.ResourceVersion {
		return fmt.Errorf("execution context target preconditions do not match proposal")
	}
	if e.ApprovalID == "" || e.IdempotencyKey == "" || time.Now().After(e.ExpiresAt) {
		return fmt.Errorf("approval context is missing or expired")
	}
	allowed := false
	for _, ns := range e.NamespaceAllowlist {
		allowed = allowed || ns == s.Incident.Namespace
	}
	if !allowed {
		return fmt.Errorf("namespace is not approved")
	}
	return nil
}
func statusNode(transition func(context.Context, *domain.Incident, domain.IncidentStatus) error, status domain.IncidentStatus) func(context.Context, *WorkflowState) (*WorkflowState, error) {
	return func(ctx context.Context, s *WorkflowState) (*WorkflowState, error) {
		return s, transition(ctx, s.Incident, status)
	}
}
func safeIncident(in *domain.Incident) map[string]any {
	return map[string]any{"id": in.ID, "severity": in.Severity, "service": in.Service, "namespace": in.Namespace, "resource": in.Resource, "summary": in.Summary, "evidence_start_at": in.EvidenceStartAt}
}
func (s *Supervisor) Run(ctx context.Context, in *domain.Incident) (*WorkflowState, error) {
	handler := workflowgraph.NewEinoCallback(in.ID, s.eventSink)
	ctx = withAgentCallbacks(ctx, handler)
	state, err := s.runnable.Invoke(ctx, &WorkflowState{Version: WorkflowVersion, Incident: in, ModelSnapshotHash: in.ModelConfigHash}, compose.WithCheckPointID("incident:"+in.ID), compose.WithRuntimeMaxSteps(40), compose.WithCallbacks(handler))
	if err == nil && s.checkpoints != nil {
		_ = s.checkpoints.Delete(ctx, "incident:"+in.ID)
	}
	return state, err
}
func (s *Supervisor) Resume(ctx context.Context, id, interruptID string, data *ApprovalResumeData) (*WorkflowState, error) {
	ctx = compose.ResumeWithData(ctx, interruptID, data)
	handler := workflowgraph.NewEinoCallback(id, s.eventSink)
	ctx = withAgentCallbacks(ctx, handler)
	state, err := s.runnable.Invoke(ctx, &WorkflowState{}, compose.WithCheckPointID("incident:"+id), compose.WithRuntimeMaxSteps(40), compose.WithCallbacks(handler))
	if err == nil && s.checkpoints != nil {
		_ = s.checkpoints.Delete(ctx, "incident:"+id)
	}
	return state, err
}
