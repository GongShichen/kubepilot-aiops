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
	"sync"
	"time"

	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	workflowgraph "github.com/kubepilot-aiops/kubepilot/graph"
	"github.com/kubepilot-aiops/kubepilot/internal/domain"
	actionexecution "github.com/kubepilot-aiops/kubepilot/internal/execution"
	rankpolicy "github.com/kubepilot-aiops/kubepilot/internal/reasoning/evidence"
	"github.com/kubepilot-aiops/kubepilot/internal/retrieval/reranker"
	"github.com/kubepilot-aiops/kubepilot/reasoning"
	"github.com/oklog/ulid/v2"
)

type Supervisor struct {
	runnable    compose.Runnable[*WorkflowState, *WorkflowState]
	checkpoints interface {
		Delete(context.Context, string) error
	}
	eventSink         workflowgraph.EventSink
	hooks             *supervisorHooks
	skillSnapshotHash string
	rankingPolicyHash string
	rerankerService   reranker.Service
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
	return s.skillSnapshotHash, s.rankingPolicyHash, rerankerHash
}

type SupervisorDeps struct {
	Collectors           map[string]Collector
	HistoricalCandidates HistoricalCandidateRetriever
	Knowledge            CausalPatternReader
	Reasoning            *reasoning.Engine
	Agents               *AgentRegistry
	Executor             Executor
	Checkpoints          compose.CheckPointStore
	Reranker             reranker.Service
	RankingPolicy        *rankpolicy.Policy
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
	if deps.Agents == nil {
		return nil, fmt.Errorf("Eino ADK agent registry is required")
	}
	if deps.Executor == nil {
		return nil, fmt.Errorf("recovery executor is required")
	}
	if deps.Reasoning == nil {
		deps.Reasoning = reasoning.New(reasoning.DefaultConfig())
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
			hooks.eventSink(ctx, workflowgraph.WorkflowEvent{IncidentID: incident.ID, RunID: ulid.Make().String(), Type: "status_transition", Name: string(to), Component: "EinoConstrainedReAct", OccurredAt: time.Now().UTC()})
		}
		return nil
	}
	g := compose.NewGraph[*WorkflowState, *WorkflowState](compose.WithGenLocalState(func(context.Context) *WorkflowState { return &WorkflowState{Workflow: WorkflowName} }))
	add := func(name string, fn func(context.Context, *WorkflowState) (*WorkflowState, error)) error {
		return g.AddLambdaNode(name, compose.InvokableLambda(fn), compose.WithNodeName(name))
	}
	if err := add("incident_intake", func(ctx context.Context, s *WorkflowState) (*WorkflowState, error) {
		s.Workflow = WorkflowName
		s.Incident.SkillSnapshotHash = deps.Agents.SkillSnapshotHash()
		if deps.RankingPolicy != nil {
			s.Incident.RankingPolicyHash = deps.RankingPolicy.Hash
		}
		if deps.Reranker != nil {
			s.Incident.RerankerConfigHash = deps.Reranker.ConfigHash()
		}
		if err := transition(ctx, s.Incident, domain.StatusCorrelating); err != nil {
			return s, err
		}
		if err := transition(ctx, s.Incident, domain.StatusCollecting); err != nil {
			return s, err
		}
		return s, compose.ProcessState[*WorkflowState](ctx, func(_ context.Context, local *WorkflowState) error { *local = *s; return nil })
	}); err != nil {
		return nil, err
	}
	if err := add("constrained_react_agents", func(ctx context.Context, s *WorkflowState) (*WorkflowState, error) {
		if err := deps.Agents.RunConstrained(ctx, s, constrainedToolDeps{Collectors: deps.Collectors, Historical: deps.HistoricalCandidates, Knowledge: deps.Knowledge, Reasoning: deps.Reasoning, Executor: deps.Executor, Reranker: deps.Reranker, Policy: deps.RankingPolicy, Transition: transition}); err != nil {
			return s, err
		}
		s.Incident.DiagnosisLedger = &s.DiagnosisLedger
		return s, nil
	}); err != nil {
		return nil, err
	}
	if err := add("deterministic_proposal_validator", func(ctx context.Context, s *WorkflowState) (*WorkflowState, error) {
		if s.Incident.Status == domain.StatusNeedsAttention {
			return s, nil
		}
		if s.Incident.Status != domain.StatusProposing || s.Incident.Proposal == nil || s.DryRun == nil || !s.DryRun.Success || s.Incident.DryRun == nil || s.Incident.DryRun.MutationSpecHash != s.DryRun.MutationSpecHash {
			s.Errors = append(s.Errors, "constrained Agent handoff did not contain a matching accepted proposal and dry-run")
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
	if err := add("deterministic_action_executor", func(ctx context.Context, s *WorkflowState) (*WorkflowState, error) {
		if err := validateExecutionContext(s); err != nil {
			s.Errors = append(s.Errors, err.Error())
			_ = transition(ctx, s.Incident, domain.StatusNeedsAttention)
			return s, nil
		}
		if err := actionExecutor.Execute(ctx, s.Incident, *s.Incident.Proposal); err != nil {
			status := domain.StatusRecoveryFailed
			if errors.Is(err, ErrActionResultUnknown) {
				status = domain.StatusNeedsAttention
			}
			s.Errors = append(s.Errors, err.Error())
			_ = transition(ctx, s.Incident, status)
			return s, nil
		}
		if err := transition(ctx, s.Incident, domain.StatusVerifying); err != nil {
			return s, err
		}
		s.VerificationState.StartedAt = time.Now().UTC()
		return s, nil
	}); err != nil {
		return nil, err
	}
	if err := add("deterministic_verification_controller", func(ctx context.Context, s *WorkflowState) (*WorkflowState, error) {
		return runVerificationController(ctx, s, deps, transition)
	}); err != nil {
		return nil, err
	}
	if err := add("incident_finalizer", func(_ context.Context, s *WorkflowState) (*WorkflowState, error) {
		s.Incident.UpdatedAt = time.Now().UTC()
		s.Incident.DiagnosisLedger = &s.DiagnosisLedger
		return s, nil
	}); err != nil {
		return nil, err
	}
	for _, edge := range [][2]string{{compose.START, "incident_intake"}, {"incident_intake", "constrained_react_agents"}, {"constrained_react_agents", "deterministic_proposal_validator"}} {
		if err := g.AddEdge(edge[0], edge[1]); err != nil {
			return nil, err
		}
	}
	if err := g.AddBranch("deterministic_proposal_validator", compose.NewGraphBranch(func(_ context.Context, s *WorkflowState) (string, error) {
		if s.Incident.Status == domain.StatusNeedsAttention {
			return "incident_finalizer", nil
		}
		return "approval_interrupt", nil
	}, map[string]bool{"incident_finalizer": true, "approval_interrupt": true})); err != nil {
		return nil, err
	}
	if err := g.AddBranch("approval_interrupt", compose.NewGraphBranch(func(_ context.Context, s *WorkflowState) (string, error) {
		if s.Incident.Status == domain.StatusRejected {
			return "incident_finalizer", nil
		}
		return "deterministic_action_executor", nil
	}, map[string]bool{"incident_finalizer": true, "deterministic_action_executor": true})); err != nil {
		return nil, err
	}
	if err := g.AddBranch("deterministic_action_executor", compose.NewGraphBranch(func(_ context.Context, s *WorkflowState) (string, error) {
		if s.Incident.Status != domain.StatusVerifying {
			return "incident_finalizer", nil
		}
		return "deterministic_verification_controller", nil
	}, map[string]bool{"incident_finalizer": true, "deterministic_verification_controller": true})); err != nil {
		return nil, err
	}
	if err := g.AddEdge("deterministic_verification_controller", "incident_finalizer"); err != nil {
		return nil, err
	}
	if err := g.AddEdge("incident_finalizer", compose.END); err != nil {
		return nil, err
	}
	options := []compose.GraphCompileOption{compose.WithGraphName(WorkflowName)}
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
	rankingHash := ""
	if deps.RankingPolicy != nil {
		rankingHash = deps.RankingPolicy.Hash
	}
	return &Supervisor{runnable: run, checkpoints: deleter, hooks: hooks, skillSnapshotHash: deps.Agents.SkillSnapshotHash(), rankingPolicyHash: rankingHash, rerankerService: deps.Reranker}, nil
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
			evidence, err := collector.Collect(ctx, incident)
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
	if e.Confidence <= 0 || e.Confidence > 1 {
		e.Confidence = 1
	}
	raw, _ := json.Marshal(e.Content)
	h := sha256.Sum256(append([]byte(e.Source+e.Type+e.Resource+e.WindowStart.UTC().Format(time.RFC3339Nano)+e.WindowEnd.UTC().Format(time.RFC3339Nano)), raw...))
	e.ID = hex.EncodeToString(h[:])
}

func mergeEvidenceToolMessages(s *WorkflowState, messages []*schema.Message) error {
	successful := map[string]bool{}
	seen := map[string]bool{}
	for _, item := range s.Incident.Evidence {
		seen[item.ID] = true
	}
	for _, message := range messages {
		var result evidenceToolResult
		if err := json.Unmarshal([]byte(message.Content), &result); err != nil {
			return err
		}
		if result.Error != "" {
			s.Errors = append(s.Errors, result.Source+": "+result.Error)
			continue
		}
		successful[result.Source] = true
		for _, item := range result.Evidence {
			item.Source = map[string]string{"metric": "prometheus", "log": "loki", "trace": "jaeger", "kubernetes": "kubernetes", "historical": "historical"}[result.Source]
			normalizeEvidence(&item, s.Incident)
			if !seen[item.ID] {
				seen[item.ID] = true
				s.Incident.Evidence = append(s.Incident.Evidence, item)
			}
		}
	}
	if !successful["kubernetes"] {
		return fmt.Errorf("kubernetes evidence unavailable")
	}
	sort.SliceStable(s.Incident.Evidence, func(i, j int) bool { return s.Incident.Evidence[i].Timestamp.Before(s.Incident.Evidence[j].Timestamp) })
	return nil
}

type evidenceToolResult struct {
	Source   string            `json:"source"`
	Evidence []domain.Evidence `json:"evidence,omitempty"`
	Error    string            `json:"error,omitempty"`
}

func legacyHypotheses(drafts []domain.HypothesisDraft) []domain.Hypothesis {
	out := make([]domain.Hypothesis, 0, len(drafts))
	for _, draft := range drafts {
		out = append(out, domain.Hypothesis{ID: draft.ID, Cause: draft.Cause, Probability: draft.PriorProbability, SupportingEvidence: append([]string(nil), draft.SupportingEvidenceIDs...), ContradictingEvidence: append([]string(nil), draft.ContradictingEvidenceIDs...), FalsificationConditions: append([]string(nil), draft.FalsificationConditions...)})
	}
	return out
}

type diagnosisEvidence struct {
	ID             string         `json:"id"`
	Source         string         `json:"source"`
	Type           string         `json:"type"`
	Timestamp      time.Time      `json:"timestamp,omitempty"`
	Namespace      string         `json:"namespace,omitempty"`
	Service        string         `json:"service,omitempty"`
	Resource       string         `json:"resource,omitempty"`
	Summary        string         `json:"summary"`
	Content        map[string]any `json:"content,omitempty"`
	RelevanceScore float64        `json:"relevance_score"`
	RankingReasons []string       `json:"ranking_reasons,omitempty"`
	CausalNodeIDs  []string       `json:"causal_node_ids,omitempty"`
}

func diagnosisEvidenceContext(items []domain.Evidence) []diagnosisEvidence {
	out := make([]diagnosisEvidence, 0, len(items))
	for _, item := range items {
		out = append(out, diagnosisEvidence{ID: item.ID, Source: item.Source, Type: item.Type, Timestamp: item.Timestamp, Namespace: item.Namespace, Service: item.Service, Resource: item.Resource, Summary: item.Summary, Content: item.Content, RelevanceScore: item.RelevanceScore, RankingReasons: append([]string(nil), item.RankingReasons...), CausalNodeIDs: append([]string(nil), item.CausalNodeIDs...)})
	}
	return out
}

type diagnosisPattern struct {
	ID         string              `json:"id"`
	Category   string              `json:"category"`
	Cause      string              `json:"cause"`
	NodeIDs    []string            `json:"node_ids"`
	Edges      []domain.CausalEdge `json:"edges"`
	Confidence float64             `json:"confidence"`
}

func diagnosisPatternContext(items []domain.CausalPattern) []diagnosisPattern {
	out := make([]diagnosisPattern, 0, len(items))
	for _, item := range items {
		nodes := make([]string, 0, len(item.Nodes))
		for _, node := range item.Nodes {
			nodes = append(nodes, node.ID)
		}
		out = append(out, diagnosisPattern{ID: item.ID, Category: item.Category, Cause: item.Cause, NodeIDs: nodes, Edges: append([]domain.CausalEdge(nil), item.Edges...), Confidence: item.Confidence})
	}
	return out
}

type diagnosisCandidate struct {
	IncidentID       string               `json:"incident_id"`
	Namespace        string               `json:"namespace"`
	Service          string               `json:"service"`
	Resource         string               `json:"resource"`
	Category         string               `json:"category"`
	RootCause        string               `json:"root_cause"`
	Summary          string               `json:"summary,omitempty"`
	Rank             domain.RankBreakdown `json:"rank"`
	RankingReasons   []string             `json:"ranking_reasons,omitempty"`
	TopologyServices []string             `json:"topology_services,omitempty"`
	CausalNodeIDs    []string             `json:"causal_node_ids,omitempty"`
}

func diagnosisCandidateContext(items []domain.RetrievalCandidate) []diagnosisCandidate {
	out := make([]diagnosisCandidate, 0, len(items))
	for _, item := range items {
		out = append(out, diagnosisCandidate{IncidentID: item.IncidentID, Namespace: item.Namespace, Service: item.Service, Resource: item.Resource, Category: item.Category, RootCause: item.RootCause, Summary: item.Summary, Rank: item.Rank, RankingReasons: append([]string(nil), item.RankingReasons...), TopologyServices: append([]string(nil), item.Features.TopologyServices...), CausalNodeIDs: append([]string(nil), item.Features.CausalNodeIDs...)})
	}
	return out
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
func safeIncident(in *domain.Incident) map[string]any {
	return map[string]any{"id": in.ID, "severity": in.Severity, "service": in.Service, "namespace": in.Namespace, "resource": in.Resource, "summary": in.Summary, "evidence_start_at": in.EvidenceStartAt}
}

func (s *Supervisor) Run(ctx context.Context, in *domain.Incident) (*WorkflowState, error) {
	handler := workflowgraph.NewEinoCallback(in.ID, s.eventSink)
	ctx = withAgentCallbacks(ctx, handler)
	state, err := s.runnable.Invoke(ctx, &WorkflowState{Workflow: WorkflowName, Incident: in, ModelSnapshotHash: in.ModelConfigHash}, compose.WithCheckPointID("incident:"+in.ID), compose.WithRuntimeMaxSteps(GraphMaxSteps), compose.WithCallbacks(handler))
	if err == nil && s.checkpoints != nil {
		_ = s.checkpoints.Delete(ctx, "incident:"+in.ID)
	}
	return state, err
}
func (s *Supervisor) Resume(ctx context.Context, id, interruptID string, data *ApprovalResumeData) (*WorkflowState, error) {
	ctx = compose.ResumeWithData(ctx, interruptID, data)
	handler := workflowgraph.NewEinoCallback(id, s.eventSink)
	ctx = withAgentCallbacks(ctx, handler)
	// A checkpoint resume must use a nil input. Supplying a fresh empty state
	// would replace the state restored by Eino before the interrupt boundary and
	// can cause upstream collection nodes to run again.
	state, err := s.runnable.Invoke(ctx, nil, compose.WithCheckPointID("incident:"+id), compose.WithRuntimeMaxSteps(GraphMaxSteps), compose.WithCallbacks(handler))
	if err == nil && s.checkpoints != nil {
		_ = s.checkpoints.Delete(ctx, "incident:"+id)
	}
	return state, err
}
