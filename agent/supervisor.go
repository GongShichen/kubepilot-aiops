package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	workflowgraph "github.com/kubepilot-aiops/kubepilot/graph"
	"github.com/kubepilot-aiops/kubepilot/internal/brainruntime"
	"github.com/kubepilot-aiops/kubepilot/internal/domain"
	evidencenorm "github.com/kubepilot-aiops/kubepilot/internal/evidence"
	actionexecution "github.com/kubepilot-aiops/kubepilot/internal/execution"
	"github.com/kubepilot-aiops/kubepilot/internal/retrieval/reranker"
	"github.com/kubepilot-aiops/kubepilot/internal/topology"
	"github.com/kubepilot-aiops/kubepilot/reasoning"
	retrievalpipeline "github.com/kubepilot-aiops/kubepilot/retrieval"
	captools "github.com/kubepilot-aiops/kubepilot/tools"
	"github.com/oklog/ulid/v2"
)

type Supervisor struct {
	runnable    compose.Runnable[*WorkflowState, *WorkflowState]
	checkpoints interface {
		Delete(context.Context, string) error
	}
	eventSink       workflowgraph.EventSink
	hooks           *supervisorHooks
	brainSkillHash  string
	brainToolHash   string
	brainPolicyHash string
	rerankerService reranker.Service
	brainStates     *sync.Map
	brainRuntime    *brainGraphRuntime
}

type supervisorHooks struct{ eventSink workflowgraph.EventSink }

func (s *Supervisor) SetEventSink(sink workflowgraph.EventSink) {
	s.eventSink = sink
	if s.hooks != nil {
		s.hooks.eventSink = sink
	}
}
func (s *Supervisor) RuntimeHashes() (string, string, string) {
	rerankerHash := ""
	if s.rerankerService != nil {
		rerankerHash = s.rerankerService.ConfigHash()
	}
	return s.brainSkillHash, s.brainPolicyHash, rerankerHash
}

// RuntimeHashesForMethod is retained as the incident-manager boundary, but the
// production Supervisor accepts only KubePilot Brain methods. There is no
// baseline hash selection or legacy runtime fallback.
func (s *Supervisor) RuntimeHashesForMethod(method string) (string, string, string) {
	return s.RuntimeHashes()
}

// BrainExecutionSnapshot returns the immutable executable dependency set for
// a new Workflow Attempt. It does not mutate or validate an existing attempt.
func (s *Supervisor) BrainExecutionSnapshot(modelConfigHash string) domain.ExecutionSnapshot {
	return domain.ExecutionSnapshot{SkillSnapshotHash: s.brainSkillHash, ModelConfigHash: modelConfigHash, ToolSchemaHash: s.brainToolHash, PolicyHash: s.brainPolicyHash}
}

type SupervisorDeps struct {
	Collectors           map[string]Collector
	BrainRetrieval       BrainHybridRetriever
	SkillRetrieval       BrainSkillRetriever
	Knowledge            CausalPatternReader
	Reasoning            *reasoning.Engine
	BrainModel           BrainModelRuntime
	Executor             Executor
	Checkpoints          compose.CheckPointStore
	Reranker             reranker.Service
	GraphStore           topology.GraphStore
	Memory               MemoryService
	ExternalInventory    []domain.ResourceRef
	VerificationInterval time.Duration
	VerificationTimeout  time.Duration
}

type approvalInterruptState struct{ State *WorkflowState }
type ApprovalResumeData struct {
	Approved bool                    `json:"approved"`
	Context  domain.ExecutionContext `json:"execution_context"`
}

func init() {
	schema.RegisterName[*WorkflowState]("kubepilot_incident_state")
	schema.RegisterName[*approvalInterruptState]("kubepilot_approval_interrupt")
	schema.RegisterName[*ApprovalResumeData]("kubepilot_approval_resume")
	schema.RegisterName[*domain.AgentBudgetState]("kubepilot_agent_budget")
	schema.RegisterName[*domain.SafetyFeedback]("kubepilot_safety_feedback")
	schema.RegisterName[*domain.HypothesisConfidenceRecord]("kubepilot_hypothesis_confidence")
}

func NewSupervisor(ctx context.Context, deps SupervisorDeps) (*Supervisor, error) {
	if deps.BrainModel.Chat == nil {
		return nil, fmt.Errorf("KubePilot Brain Eino ChatModel is required")
	}
	if deps.Executor == nil {
		return nil, fmt.Errorf("recovery executor is required")
	}
	if deps.Reasoning == nil {
		deps.Reasoning = reasoning.New(reasoning.DefaultConfig())
	}
	if deps.GraphStore == nil {
		deps.GraphStore = topology.NewMemoryStore()
	}
	if deps.VerificationInterval <= 0 {
		deps.VerificationInterval = 10 * time.Second
	}
	if deps.VerificationTimeout <= 0 {
		deps.VerificationTimeout = 2 * time.Minute
	}
	actionExecutor, err := actionexecution.NewActionExecutor(ctx, deps.Executor, func(incident *domain.Incident, proposal domain.RecoveryProposal) error {
		return validateExecutionContext(&WorkflowState{Incident: incident, DryRun: incident.DryRun, ExecutionContext: incident.ExecutionContext})
	})
	if err != nil {
		return nil, err
	}
	hooks := &supervisorHooks{}
	transition := func(ctx context.Context, incident *domain.Incident, to domain.IncidentStatus) error {
		from := incident.Status
		if err := domain.Transition(incident, to); err != nil {
			return err
		}
		if hooks.eventSink != nil && from != to {
			hooks.eventSink(ctx, workflowgraph.WorkflowEvent{IncidentID: incident.ID, RunID: ulid.Make().String(), Type: "status_transition", Name: string(to), Component: domain.RuntimeNameForDiagnosisMethod(incident.DiagnosisMethod), OccurredAt: time.Now().UTC()})
		}
		return nil
	}
	brainResolver, err := LoadDefaultBrainSkillResolver()
	if err != nil {
		return nil, err
	}
	if deps.SkillRetrieval == nil {
		// Skill retrieval is a mandatory part of the new Brain path. A local
		// BM25 index remains available when an embedding provider is absent; this
		// is the same retrieval architecture with one unavailable channel, never
		// direct catalog injection.
		deps.SkillRetrieval = retrievalpipeline.NewSkillHybridRetriever(nil, deps.Reranker)
	}
	brainDeps := brainRuntimeDeps{Collectors: deps.Collectors, BrainRetrieval: deps.BrainRetrieval, SkillRetrieval: deps.SkillRetrieval, Knowledge: deps.Knowledge, Reasoning: deps.Reasoning, Executor: deps.Executor, GraphStore: deps.GraphStore, Memory: deps.Memory, ExternalInventory: append([]domain.ResourceRef(nil), deps.ExternalInventory...), Transition: transition}
	brainTools, err := buildBrainCapabilities(brainDeps, brainResolver)
	if err != nil {
		return nil, err
	}
	brainToolHash, err := brainTools.SchemaHash(ctx, captools.NodeBrainEvidence, captools.NodeBrainRetrieval, captools.NodeBrainReasoning, captools.NodeBrainRecovery, captools.NodeBrainControl)
	if err != nil {
		return nil, err
	}
	brainStates := &sync.Map{}
	brainRuntime := &brainGraphRuntime{resolver: brainResolver, tools: brainTools, toolHash: brainToolHash, policyHash: brainruntime.Hash(brainResolver.ToolPolicy()), model: deps.BrainModel, deps: brainDeps, states: brainStates}
	brainGraph, err := buildBrainGraph(ctx, brainRuntime)
	if err != nil {
		return nil, err
	}
	// WorkflowState is the only state source. The production graph contains
	// only the new KubePilot Brain; no baseline subgraph is compiled into this
	// runnable and no method can fall through to an older diagnosis path.
	g := compose.NewGraph[*WorkflowState, *WorkflowState]()
	add := func(name string, fn func(context.Context, *WorkflowState) (*WorkflowState, error)) error {
		return g.AddLambdaNode(name, compose.InvokableLambda(fn), compose.WithNodeName(name))
	}
	if err := g.AddGraphNode("kubepilot_brain", brainGraph,
		compose.WithGraphCompileOptions(
			compose.WithGraphName("kubepilot_brain_loop"),
			compose.WithMaxRunSteps(BrainGraphMaxSteps),
		),
		compose.WithNodeName("kubepilot_brain_loop"),
	); err != nil {
		return nil, err
	}
	if err := add("incident_intake", func(ctx context.Context, s *WorkflowState) (*WorkflowState, error) {
		method, valid := domain.NormalizeDiagnosisMethod(s.Incident.DiagnosisMethod)
		if !valid || !domain.IsKubePilotBrainMethod(method) {
			return s, fmt.Errorf("diagnosis method %q is not supported by the KubePilot Brain runtime", s.Incident.DiagnosisMethod)
		}
		s.Workflow = domain.BrainWorkflowRuntimeName
		s.Incident.DiagnosisMethod = method
		s.Incident.SkillSnapshotHash = brainResolver.SnapshotHash()
		if deps.Reranker != nil {
			s.Incident.RerankerConfigHash = deps.Reranker.ConfigHash()
		}
		if err := transition(ctx, s.Incident, domain.StatusCorrelating); err != nil {
			return s, err
		}
		if err := transition(ctx, s.Incident, domain.StatusCollecting); err != nil {
			return s, err
		}
		return s, nil
	}); err != nil {
		return nil, err
	}
	if err := add("brain_recovery_permission", func(ctx context.Context, s *WorkflowState) (*WorkflowState, error) {
		return brainRecoveryPermissionNode(ctx, s, transition)
	}); err != nil {
		return nil, err
	}
	if err := add("brain_dry_run", func(ctx context.Context, s *WorkflowState) (*WorkflowState, error) {
		return brainDryRunNode(ctx, s, brainDeps.Executor, transition)
	}); err != nil {
		return nil, err
	}
	if err := add("recovery_proposal_validator", func(ctx context.Context, s *WorkflowState) (*WorkflowState, error) {
		if s.Incident.Status == domain.StatusNeedsAttention {
			return s, nil
		}
		if s.Incident.Status != domain.StatusProposing || s.Incident.Proposal == nil || s.DryRun == nil || !s.DryRun.Success || s.Incident.DryRun == nil || s.Incident.DryRun.MutationSpecHash != s.DryRun.MutationSpecHash {
			s.Errors = append(s.Errors, "diagnosis handoff did not contain a matching accepted proposal and dry-run")
			if err := transition(ctx, s.Incident, domain.StatusNeedsAttention); err != nil {
				return s, err
			}
		}
		return s, nil
	}); err != nil {
		return nil, err
	}
	if err := add("approval_interrupt", func(ctx context.Context, s *WorkflowState) (*WorkflowState, error) {
		return approvalNode(ctx, s, transition)
	}); err != nil {
		return nil, err
	}
	if err := add("safety_action_executor", func(ctx context.Context, s *WorkflowState) (*WorkflowState, error) {
		if err := validateExecutionContext(s); err != nil {
			s.Errors = append(s.Errors, err.Error())
			_ = transition(ctx, s.Incident, domain.StatusNeedsAttention)
			return s, nil
		}
		if s.Incident.RecoveryExecution == nil {
			s.Incident.RecoveryExecution = &domain.RecoveryExecution{}
		}
		execution := s.Incident.RecoveryExecution
		execution.Attempts++
		execution.Namespace = s.Incident.Proposal.Namespace
		execution.Target = s.Incident.Proposal.Target
		execution.Action = string(s.Incident.Proposal.Action)
		execution.Outcome = "attempting"
		execution.LastAttemptAt = time.Now().UTC()
		if err := actionExecutor.Execute(ctx, s.Incident, *s.Incident.Proposal); err != nil {
			status := domain.StatusRecoveryFailed
			if errors.Is(err, ErrActionResultUnknown) {
				status = domain.StatusNeedsAttention
				execution.Outcome = "unknown"
				setFinalBrainTermination(s, domain.TerminationExecutionOutcomeUnknown, []string{"recovery mutation outcome could not be confirmed"})
			} else {
				execution.Outcome = "failed"
				if brainReflectionAvailable(s, domain.ReflectionRecoveryFailure) {
					trigger := domain.ReflectionRecoveryFailure
					s.PendingReflection = &trigger
					s.PendingTermination = domain.TerminationRecoveryFailed
					s.BrainPhase = domain.BrainPhaseReflection
					s.ResumeBrainPhase = domain.BrainPhaseEscalation
					s.Termination = nil
					if s.Incident.Investigation != nil {
						s.Incident.Investigation.Termination = nil
					}
				} else {
					setFinalBrainTermination(s, domain.TerminationRecoveryFailed, []string{"registered recovery action failed"})
				}
			}
			s.Errors = append(s.Errors, err.Error())
			appendBrainStateChange(s, "execute_recovery", strings.ToUpper(execution.Outcome), "recovery execution did not produce a confirmed mutation", true, brainApprovalID(s))
			_ = transition(ctx, s.Incident, status)
			return s, nil
		}
		execution.ConfirmedMutations++
		execution.Outcome = "succeeded"
		execution.CompletedAt = time.Now().UTC()
		appendBrainStateChange(s, "execute_recovery", "MUTATION_CONFIRMED", "registered Kubernetes mutation completed", true, brainApprovalID(s))
		if err := transition(ctx, s.Incident, domain.StatusVerifying); err != nil {
			return s, err
		}
		s.VerificationState.StartedAt = time.Now().UTC()
		return s, nil
	}); err != nil {
		return nil, err
	}
	if err := add("verification_controller", func(ctx context.Context, s *WorkflowState) (*WorkflowState, error) {
		return runVerificationController(ctx, s, deps, transition)
	}); err != nil {
		return nil, err
	}
	if err := add("incident_finalizer", func(_ context.Context, s *WorkflowState) (*WorkflowState, error) {
		s.Incident.UpdatedAt = time.Now().UTC()
		// The Brain runtime has a dedicated audit projection. The deterministic
		// diagnosis ledger is never attached or dual-written.
		s.Incident.DiagnosisLedger = nil
		if s.WorkflowAttempt != nil {
			s.WorkflowAttempt.Status = domain.WorkflowAttemptCompleted
			s.WorkflowAttempt.CompletedAt = time.Now().UTC()
			s.Incident.WorkflowAttempt = s.WorkflowAttempt
			if s.Incident.Investigation != nil {
				s.Incident.Investigation.WorkflowAttempt = s.WorkflowAttempt
			}
		}
		return s, nil
	}); err != nil {
		return nil, err
	}
	for _, edge := range [][2]string{
		{compose.START, "incident_intake"},
		{"incident_intake", "kubepilot_brain"},
	} {
		if err := g.AddEdge(edge[0], edge[1]); err != nil {
			return nil, err
		}
	}
	if err := g.AddEdge("kubepilot_brain", "brain_recovery_permission"); err != nil {
		return nil, err
	}
	if err := g.AddBranch("brain_recovery_permission", compose.NewGraphBranch(func(_ context.Context, s *WorkflowState) (string, error) {
		if s == nil || s.Incident == nil {
			return "", fmt.Errorf("Brain recovery permission is missing workflow state")
		}
		if s.Incident.Status == domain.StatusProposing && s.Incident.Proposal != nil {
			return "brain_dry_run", nil
		}
		return "incident_finalizer", nil
	}, map[string]bool{"brain_dry_run": true, "incident_finalizer": true})); err != nil {
		return nil, err
	}
	if err := g.AddBranch("brain_dry_run", compose.NewGraphBranch(func(_ context.Context, s *WorkflowState) (string, error) {
		if s != nil && s.DryRun != nil && s.DryRun.Success && s.Incident != nil && s.Incident.Proposal != nil {
			return "recovery_proposal_validator", nil
		}
		return "kubepilot_brain", nil
	}, map[string]bool{"recovery_proposal_validator": true, "kubepilot_brain": true})); err != nil {
		return nil, err
	}
	if err := g.AddBranch("recovery_proposal_validator", compose.NewGraphBranch(func(_ context.Context, s *WorkflowState) (string, error) {
		if s.Incident.Status == domain.StatusNeedsAttention {
			return "incident_finalizer", nil
		}
		return "approval_interrupt", nil
	}, map[string]bool{"incident_finalizer": true, "approval_interrupt": true})); err != nil {
		return nil, err
	}
	if err := g.AddBranch("approval_interrupt", compose.NewGraphBranch(func(_ context.Context, s *WorkflowState) (string, error) {
		if s.Incident.Status == domain.StatusRejected || s.Incident.Status == domain.StatusNeedsAttention {
			return "incident_finalizer", nil
		}
		return "safety_action_executor", nil
	}, map[string]bool{"incident_finalizer": true, "safety_action_executor": true})); err != nil {
		return nil, err
	}
	if err := g.AddBranch("safety_action_executor", compose.NewGraphBranch(func(_ context.Context, s *WorkflowState) (string, error) {
		if s.PendingReflection != nil {
			return "kubepilot_brain", nil
		}
		if s.Incident.Status != domain.StatusVerifying {
			return "incident_finalizer", nil
		}
		return "verification_controller", nil
	}, map[string]bool{"incident_finalizer": true, "verification_controller": true, "kubepilot_brain": true})); err != nil {
		return nil, err
	}
	if err := g.AddBranch("verification_controller", compose.NewGraphBranch(func(_ context.Context, s *WorkflowState) (string, error) {
		if s.PendingReflection != nil {
			return "kubepilot_brain", nil
		}
		return "incident_finalizer", nil
	}, map[string]bool{"incident_finalizer": true, "kubepilot_brain": true})); err != nil {
		return nil, err
	}
	if err := g.AddEdge("incident_finalizer", compose.END); err != nil {
		return nil, err
	}
	options := []compose.GraphCompileOption{compose.WithGraphName(domain.SupervisorRuntimeName), compose.WithMaxRunSteps(BrainGraphMaxSteps)}
	if deps.Checkpoints != nil {
		options = append(options, compose.WithCheckPointStore(deps.Checkpoints))
	}
	run, err := g.Compile(ctx, options...)
	if err != nil {
		return nil, err
	}
	var deleter interface {
		Delete(context.Context, string) error
	}
	if candidate, ok := deps.Checkpoints.(interface {
		Delete(context.Context, string) error
	}); ok {
		deleter = candidate
	}
	return &Supervisor{runnable: run, checkpoints: deleter, hooks: hooks, brainSkillHash: brainResolver.SnapshotHash(), brainToolHash: brainToolHash, brainPolicyHash: brainRuntime.policyHash, rerankerService: deps.Reranker, brainStates: brainStates, brainRuntime: brainRuntime}, nil
}

type verificationRoundResult struct {
	source       string
	verification *domain.Verification
	probe        verificationProbeResult
	err          error
}

func runVerificationController(ctx context.Context, state *WorkflowState, deps SupervisorDeps, transition func(context.Context, *domain.Incident, domain.IncidentStatus) error) (*WorkflowState, error) {
	if state.VerificationState.StartedAt.IsZero() {
		state.VerificationState.StartedAt = time.Now().UTC()
	}
	deadline := state.VerificationState.StartedAt.Add(deps.VerificationTimeout)
	var previousRestarts *int32
	for {
		combined, infrastructureErrors := collectVerificationRound(ctx, state.Incident, deps)
		for _, message := range infrastructureErrors {
			state.Errors = appendUnique(state.Errors, message)
		}
		if combined.RestartCount != nil {
			stable := previousRestarts == nil || *combined.RestartCount <= *previousRestarts
			combined.Checks["restarts_stable"] = stable
			combined.Success = combined.Success && stable
			value := *combined.RestartCount
			previousRestarts = &value
		}
		combined.CompletedAt = time.Now().UTC()
		state.Incident.Verification = &combined
		state.VerificationState.Attempts++
		if combined.Success {
			state.VerificationState.ConsecutiveSuccess++
		} else {
			state.VerificationState.ConsecutiveSuccess = 0
		}
		if state.VerificationState.ConsecutiveSuccess >= 3 {
			combined.Message = "all applicable recovery checks passed three consecutive rounds"
			state.Incident.Verification = &combined
			if err := transition(ctx, state.Incident, domain.StatusResolved); err != nil {
				return state, err
			}
			appendBrainStateChange(state, "verify_recovery", "VERIFICATION_SUCCEEDED", combined.Message, true, brainApprovalID(state))
			setFinalBrainTermination(state, domain.TerminationRecoverySucceeded, nil)
			return state, nil
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			combined.Success = false
			combined.Message = "verification timed out before three consecutive successful rounds"
			state.Incident.Verification = &combined
			if err := transition(ctx, state.Incident, domain.StatusRecoveryFailed); err != nil {
				return state, err
			}
			appendBrainStateChange(state, "verify_recovery", "VERIFICATION_FAILED", combined.Message, true, brainApprovalID(state))
			if brainReflectionAvailable(state, domain.ReflectionVerificationFail) {
				trigger := domain.ReflectionVerificationFail
				state.PendingReflection = &trigger
				state.PendingTermination = domain.TerminationRecoveryFailed
				state.BrainPhase = domain.BrainPhaseReflection
				state.ResumeBrainPhase = domain.BrainPhaseEscalation
				state.Termination = nil
				if state.Incident.Investigation != nil {
					state.Incident.Investigation.Termination = nil
				}
			} else {
				setFinalBrainTermination(state, domain.TerminationRecoveryFailed, []string{combined.Message})
			}
			return state, nil
		}
		wait := deps.VerificationInterval
		if remaining < wait {
			wait = remaining
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return state, ctx.Err()
		case <-timer.C:
		}
	}
}

func collectVerificationRound(ctx context.Context, incident *domain.Incident, deps SupervisorDeps) (domain.Verification, []string) {
	count := 1
	for _, source := range []string{"metric", "log", "trace", "business"} {
		if deps.Collectors[source] != nil {
			count++
		}
	}
	results := make(chan verificationRoundResult, count)
	var group sync.WaitGroup
	group.Add(1)
	go func() {
		defer group.Done()
		verification, err := deps.Executor.Verify(ctx, incident)
		results <- verificationRoundResult{source: "kubernetes", verification: &verification, err: err}
	}()
	for _, source := range []string{"metric", "log", "trace", "business"} {
		collector := deps.Collectors[source]
		if collector == nil {
			continue
		}
		source, collector := source, collector
		group.Add(1)
		go func() {
			defer group.Done()
			evidence, err := collector.Collect(ctx, incident, defaultEvidenceRequest(incident, source))
			result := verificationRoundResult{source: source, err: err}
			if err == nil {
				result.probe = evaluateVerificationEvidence(source, evidence)
			}
			results <- result
		}()
	}
	group.Wait()
	close(results)
	combined := domain.Verification{Success: true, Checks: map[string]bool{}}
	var infrastructureErrors []string
	for result := range results {
		if result.err != nil {
			combined.Success = false
			combined.Checks[result.source+"_infrastructure_ok"] = false
			infrastructureErrors = append(infrastructureErrors, result.source+" verification unavailable")
			continue
		}
		combined.Checks[result.source+"_infrastructure_ok"] = true
		if result.verification != nil {
			combined.Success = combined.Success && result.verification.Success
			for name, passed := range result.verification.Checks {
				combined.Checks[name] = passed
			}
			combined.RestartCount = result.verification.RestartCount
			continue
		}
		combined.Checks[result.source+"_applicable"] = result.probe.Applicable
		for name, passed := range result.probe.Checks {
			combined.Checks[result.source+"_"+name] = passed
		}
		if result.probe.Applicable {
			combined.Checks[result.source] = result.probe.Success
			combined.Success = combined.Success && result.probe.Success
		}
	}
	return combined, infrastructureErrors
}

func appendUnique(values []string, value string) []string {
	for _, current := range values {
		if current == value {
			return values
		}
	}
	return append(values, value)
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
		errorsFound := 0
		for _, item := range evidence {
			if item.Type == "log_entry" || item.Kind == "log_entry" {
				errorsFound++
			}
		}
		result.Success = errorsFound == 0
		result.Checks["recent_error_templates_absent"] = result.Success
		result.Message = fmt.Sprintf("%d recent error log entries remain", errorsFound)
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

type verificationProbeResult struct {
	Name       string          `json:"name"`
	Applicable bool            `json:"applicable"`
	Success    bool            `json:"success"`
	Checks     map[string]bool `json:"checks,omitempty"`
	Message    string          `json:"message,omitempty"`
}

func nonEmptyEvidenceValue(values map[string]any, key string) bool {
	if values == nil {
		return false
	}
	value, exists := values[key]
	return exists && value != nil && strings.TrimSpace(fmt.Sprint(value)) != ""
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
	// Facts is the canonical data contract. Content remains populated for
	// callers using the legacy API, while Data is no longer a second mutable
	// fact carrier that workers and rankers can accidentally disagree on.
	facts := map[string]any{}
	for _, values := range []map[string]any{e.Content, e.Data, e.Facts} {
		for key, value := range values {
			facts[key] = value
		}
	}
	if len(facts) > 0 {
		e.Facts = facts
		e.Content = make(map[string]any, len(facts))
		for key, value := range facts {
			e.Content[key] = value
		}
	} else {
		e.Facts = nil
		e.Content = nil
	}
	e.Data = nil
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
	if e.Confidence <= 0 || e.Confidence > 1 {
		e.Confidence = 1
	}
	e.ID = evidencenorm.StableID(*e)
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
		if s.WorkflowAttempt != nil {
			s.WorkflowAttempt.Status = domain.WorkflowAttemptInterrupted
			s.WorkflowAttempt.InterruptedAt = time.Now().UTC()
		}
		appendBrainStateChange(s, "approval_interrupt", "APPROVAL_REQUESTED", "recovery approval requested", true, "")
		return s, compose.StatefulInterrupt(ctx, map[string]any{"incident_id": s.Incident.ID, "proposal_id": s.Incident.Proposal.ID, "expires_at": s.Incident.Proposal.ExpiresAt}, &approvalInterruptState{State: s})
	}
	if has && state != nil && state.State != nil {
		s = state.State
	}
	isResume, hasData, data := compose.GetResumeContext[*ApprovalResumeData](ctx)
	if !isResume || !hasData || data == nil {
		return s, fmt.Errorf("approval resume data is required")
	}
	if s.WorkflowAttempt != nil {
		s.WorkflowAttempt.Status = domain.WorkflowAttemptActive
	}
	if !data.Approved {
		appendBrainStateChange(s, "approval_interrupt", "APPROVAL_REJECTED", "operator rejected recovery approval", true, "approval:"+ulid.Make().String())
		setFinalBrainTermination(s, domain.TerminationApprovalRejected, []string{"operator rejected recovery approval"})
		if err := transition(ctx, s.Incident, domain.StatusRejected); err != nil {
			return s, err
		}
		return s, nil
	}
	s.ExecutionContext = &data.Context
	s.Incident.ExecutionContext = &data.Context
	if err := validateExecutionContext(s); err != nil {
		appendBrainStateChange(s, "approval_interrupt", "APPROVAL_CONTEXT_REJECTED", "approval context did not match the frozen recovery chain", false, data.Context.ApprovalID)
		setFinalBrainTermination(s, domain.TerminationSafetyBlocked, []string{err.Error()})
		if transitionErr := transition(ctx, s.Incident, domain.StatusNeedsAttention); transitionErr != nil {
			return s, transitionErr
		}
		return s, nil
	}
	appendBrainStateChange(s, "approval_interrupt", "APPROVAL_GRANTED", "operator approved the frozen recovery plan", true, data.Context.ApprovalID)
	if err := transition(ctx, s.Incident, domain.StatusRecovering); err != nil {
		return s, err
	}
	return s, nil
}

func brainApprovalID(state *WorkflowState) string {
	if state != nil && state.ExecutionContext != nil {
		return state.ExecutionContext.ApprovalID
	}
	return ""
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
	diagnosis, plan := s.AgentDiagnosis, s.AgentRecoveryPlan
	executionSnapshot, evidenceSnapshot := s.ExecutionSnapshot, s.EvidenceSnapshotHash
	if s.Incident.Investigation != nil {
		if diagnosis == nil {
			diagnosis = s.Incident.Investigation.AgentDiagnosis
		}
		if plan == nil {
			plan = s.Incident.Investigation.AgentRecoveryPlan
		}
	}
	if executionSnapshot == (domain.ExecutionSnapshot{}) && s.Incident.ExecutionSnapshot != nil {
		executionSnapshot = *s.Incident.ExecutionSnapshot
	}
	if evidenceSnapshot == "" && plan != nil {
		evidenceSnapshot = plan.EvidenceSnapshotHash
	}
	if diagnosis == nil || plan == nil {
		return fmt.Errorf("Brain diagnosis and recovery plan are required for execution")
	}
	if e.RecoveryPlanID != plan.ID || e.DiagnosisVersion != diagnosis.ID || e.DiagnosisVersion != plan.DiagnosisVersion {
		return fmt.Errorf("execution context does not match the frozen diagnosis and recovery plan versions")
	}
	if e.EvidenceSnapshotHash != evidenceSnapshot || e.EvidenceSnapshotHash != plan.EvidenceSnapshotHash {
		return fmt.Errorf("execution context does not match the frozen Evidence snapshot")
	}
	if e.ExecutionSnapshot == nil || *e.ExecutionSnapshot != executionSnapshot || *e.ExecutionSnapshot != plan.ExecutionSnapshot {
		return fmt.Errorf("execution context does not match the frozen Execution snapshot")
	}
	if e.DryRunValidatedAt.IsZero() || !e.DryRunValidatedAt.Equal(s.DryRun.ValidatedAt) {
		return fmt.Errorf("execution context does not match the approved dry-run version")
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
func safeIncident(in *domain.Incident) map[string]any {
	return map[string]any{"id": in.ID, "severity": in.Severity, "service": in.Service, "cluster": in.Cluster, "namespace": in.Namespace, "resource": in.Resource, "summary": in.Summary, "causal_mode": in.CausalMode, "evidence_start_at": in.EvidenceStartAt}
}

func (s *Supervisor) Run(ctx context.Context, in *domain.Incident) (*WorkflowState, error) {
	if in == nil {
		return nil, fmt.Errorf("incident is required")
	}
	method, valid := domain.NormalizeDiagnosisMethod(in.DiagnosisMethod)
	if !valid || !domain.IsKubePilotBrainMethod(method) {
		return nil, fmt.Errorf("diagnosis method %q is not supported by the KubePilot Brain runtime", in.DiagnosisMethod)
	}
	in.DiagnosisMethod = method
	handler := workflowgraph.NewEinoCallback(in.ID, s.eventSink)
	ctx = withAgentCallbacks(ctx, handler)
	initial := &WorkflowState{Workflow: domain.BrainWorkflowRuntimeName, Incident: in, ModelSnapshotHash: in.ModelConfigHash, BrainState: BrainState{WorkflowAttempt: in.WorkflowAttempt}}
	if in.ExecutionSnapshot != nil {
		initial.ExecutionSnapshot = *in.ExecutionSnapshot
	}
	ctx = withBrainWorkflowState(ctx, initial)
	ctx = withBrainStateRegistry(ctx, s.brainStates, in.ID)
	if s.brainStates != nil {
		s.brainStates.Store(in.ID, initial)
	}
	state, err := s.runnable.Invoke(ctx, initial, compose.WithCheckPointID("incident:"+in.ID), compose.WithRuntimeMaxSteps(BrainGraphMaxSteps), compose.WithCallbacks(handler))
	if err != nil {
		if _, interrupted := compose.ExtractInterruptInfo(err); !interrupted {
			failed := state
			if failed == nil {
				failed = initial
			}
			if s.brainRuntime != nil {
				s.brainRuntime.finalizeGraphFailure(failed)
			}
			state = failed
		}
	}
	if err == nil && s.checkpoints != nil {
		_ = s.checkpoints.Delete(ctx, "incident:"+in.ID)
	}
	if s.brainStates != nil {
		s.brainStates.Delete(in.ID)
	}
	return state, err
}
func (s *Supervisor) Resume(ctx context.Context, id, interruptID string, data *ApprovalResumeData) (*WorkflowState, error) {
	ctx = compose.ResumeWithData(ctx, interruptID, data)
	ctx = withBrainStateRegistry(ctx, s.brainStates, id)
	handler := workflowgraph.NewEinoCallback(id, s.eventSink)
	ctx = withAgentCallbacks(ctx, handler)
	// A checkpoint resume must use a nil input. Supplying a fresh empty state
	// would replace the state restored by Eino before the interrupt boundary and
	// can cause upstream collection nodes to run again.
	state, err := s.runnable.Invoke(ctx, nil, compose.WithCheckPointID("incident:"+id), compose.WithRuntimeMaxSteps(BrainGraphMaxSteps), compose.WithCallbacks(handler))
	if err != nil {
		if _, interrupted := compose.ExtractInterruptInfo(err); !interrupted {
			failed := state
			if failed == nil && s.brainStates != nil {
				if value, ok := s.brainStates.Load(id); ok {
					failed, _ = value.(*WorkflowState)
				}
			}
			if failed != nil && failed.Incident != nil && s.brainRuntime != nil {
				s.brainRuntime.finalizeGraphFailure(failed)
				state = failed
			}
		}
	}
	if err == nil && s.checkpoints != nil {
		_ = s.checkpoints.Delete(ctx, "incident:"+id)
	}
	if s.brainStates != nil {
		s.brainStates.Delete(id)
	}
	return state, err
}
