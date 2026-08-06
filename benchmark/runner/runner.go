package runner

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/kubepilot-aiops/kubepilot/benchmark/injector"
	"github.com/kubepilot-aiops/kubepilot/benchmark/reporter"
	"github.com/kubepilot-aiops/kubepilot/benchmark/scenarios"
	"github.com/kubepilot-aiops/kubepilot/benchmark/scorer"
	"github.com/kubepilot-aiops/kubepilot/internal/domain"
	"github.com/kubepilot-aiops/kubepilot/internal/telemetry"
)

type IncidentClient interface {
	Create(context.Context, scenarios.Scenario) (*domain.Incident, error)
	Get(context.Context, string) (*domain.Incident, error)
	Approve(context.Context, *domain.Incident) error
}

type IncidentRejecter interface {
	Reject(context.Context, *domain.Incident) error
}
type Runner struct {
	Registry     *injector.Registry
	Client       IncidentClient
	AutoApprove  bool
	PollInterval time.Duration
	// MaxCaseRestarts bounds environment restarts after all request retries
	// have been exhausted. A restart always begins with a fresh injector
	// baseline and a new Incident; it never replays a Kubernetes mutation.
	MaxCaseRestarts int
	// DiagnosisTimeout lets the runner wait for the server-side request retry
	// window. A zero value keeps the scenario timeout.
	DiagnosisTimeout time.Duration
	CaseTimeout      time.Duration
	OnResult         func(reporter.CaseResult)
	DiagnosisMethod  string
	CausalMode       string
	WorkerID         string
	Gate             *ConcurrencyGate
}

const cleanupTimeout = 2 * time.Minute

func (r *Runner) Run(ctx context.Context, items []scenarios.Scenario) []reporter.CaseResult {
	out := make([]reporter.CaseResult, 0, len(items))
	for _, s := range items {
		if ctx.Err() != nil {
			break
		}
		var result reporter.CaseResult
		restarts := 0
		maxCaseRestarts := r.maxCaseRestarts()
		for {
			caseCtx, cancel := context.WithTimeout(ctx, r.caseTimeout())
			result = r.runOne(caseCtx, s)
			cancel()
			if restarts >= maxCaseRestarts || !shouldRestartCase(result) || ctx.Err() != nil {
				break
			}
			restarts++
		}
		result.CaseRestarts = restarts
		// A process-level interruption is not a completed benchmark case. The
		// deferred baseline restore has already run, so omit the partial result
		// and let a supervised resume execute the same case from the beginning.
		if ctx.Err() != nil && result.Status != "cleanup_failed" {
			break
		}
		out = append(out, result)
		if r.OnResult != nil {
			r.OnResult(result)
		}
		if result.Status == "cleanup_failed" {
			break
		}
	}
	return out
}

func (r *Runner) maxCaseRestarts() int {
	if r.MaxCaseRestarts < 0 {
		return 0
	}
	if r.MaxCaseRestarts == 0 {
		return 1
	}
	return r.MaxCaseRestarts
}

func (r *Runner) caseTimeout() time.Duration {
	if r.CaseTimeout > 0 {
		return r.CaseTimeout
	}
	return 5 * time.Minute
}

func (r *Runner) diagnosisTimeout(s scenarios.Scenario) time.Duration {
	if r.DiagnosisTimeout > s.Timeouts.Diagnosis {
		return r.DiagnosisTimeout
	}
	return s.Timeouts.Diagnosis
}

func shouldRestartCase(result reporter.CaseResult) bool {
	if result.Status == "cleanup_failed" || result.Error == "" || result.RecoveryExecuted || result.ApprovalGranted {
		return false
	}
	message := strings.ToLower(result.Error)
	// These markers represent request/stream failures after their own bounded
	// retries. Logical diagnosis rejection is deliberately not restarted. Once
	// an approval or mutation has happened, the case must never be replayed.
	for _, marker := range []string{
		"transient request failure after retries",
		"request failed after ",
		"failed to receive stream chunk",
		"context deadline exceeded",
		"connection reset",
		"broken pipe",
		"unexpected eof",
	} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}
func (r *Runner) runOne(ctx context.Context, s scenarios.Scenario) (res reporter.CaseResult) {
	started := time.Now()
	res = reporter.CaseResult{CaseID: s.ID, Seed: s.Seed, Repetition: s.Repetition, DatasetSplit: s.Split, WorkerID: r.WorkerID, Namespace: s.Namespace, Category: s.Category, Status: "failed", DiagnosisMethod: r.DiagnosisMethod, CausalMode: r.CausalMode}
	if r.Gate != nil {
		release, err := r.Gate.Acquire(ctx)
		if err != nil {
			res.Error = "concurrency gate: " + err.Error()
			return res
		}
		defer release()
	}
	h, err := r.Registry.Get(s.Injector)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	cleaned := false
	defer func() {
		res.Duration = time.Since(started)
		if !cleaned {
			if err := restoreAndVerify(h, s); err != nil {
				res.Status = "cleanup_failed"
				res.Error = join(res.Error, err.Error())
			}
		}
		res.InfrastructureFailure = infrastructureFailure(res)
	}()
	if err = h.Preflight(ctx, s); err != nil {
		res.Error = "preflight: " + err.Error()
		return res
	}
	if err = h.RestoreBaseline(ctx, s); err != nil {
		res.Error = "baseline: " + err.Error()
		return res
	}
	if err = h.Healthy(ctx, s); err != nil {
		res.Error = "unhealthy baseline: " + err.Error()
		return res
	}
	if err = h.Inject(ctx, s); err != nil {
		res.Error = "inject: " + err.Error()
		return res
	}
	faultCtx, cancel := context.WithTimeout(ctx, s.Timeouts.FaultVisible)
	defer cancel()
	if waiter, ok := h.(interface {
		WaitVisible(context.Context, scenarios.Scenario) error
	}); ok {
		if err = waiter.WaitVisible(faultCtx, s); err != nil {
			res.Error = "fault visibility: " + err.Error()
			return res
		}
	}
	in, err := r.Client.Create(faultCtx, s)
	if err != nil {
		res.Error = "create incident: " + err.Error()
		return res
	}
	res.IncidentID = in.ID
	diagnosisCtx, diagnosisCancel := context.WithTimeout(ctx, r.diagnosisTimeout(s))
	defer diagnosisCancel()
	in, err = r.waitDiagnosis(diagnosisCtx, in.ID)
	if err != nil {
		res.Error = "diagnosis: " + err.Error()
		return res
	}
	res.Score = scorer.Incident(s, in)
	res.RecoveryProposed = in.Proposal != nil
	res.ApprovalRequested = in.Status == domain.StatusAwaitingApproval
	res.SafetyBlocked = res.RecoveryProposed && !res.Score.DecisionCorrect
	res.DryRunSuccess = in.DryRun != nil && in.DryRun.Success
	res.RootCauseCategory = in.RootCauseCategory
	res.RootCauseVariant = in.RootCauseVariant
	res.Service = in.RootCauseService
	res.Resource = in.RootCauseResource
	res.Confidence = in.Confidence
	populateAgentMetrics(&res, in)
	if in.DiagnosisError != "" {
		res.Error = "diagnosis workflow: " + in.DiagnosisError
	}
	if r.AutoApprove && res.Score.DecisionCorrect && in.Status == domain.StatusAwaitingApproval {
		res.ApprovalGranted = true
		if err = r.Client.Approve(ctx, in); err != nil {
			res.Error = "approve: " + err.Error()
			return res
		}
		recoveryCtx, recoveryCancel := context.WithTimeout(ctx, s.Timeouts.Recovery)
		defer recoveryCancel()
		recoveryStarted := time.Now()
		in, err = r.waitFinal(recoveryCtx, in.ID)
		if err != nil {
			res.Error = "recovery: " + err.Error()
			return res
		}
		res.RecoveryExecuted = true
		res.VerificationOK = in.Status == domain.StatusResolved
		res.RecoveryDurationMS = time.Since(recoveryStarted).Seconds() * 1000
	} else if r.AutoApprove && in.Status == domain.StatusAwaitingApproval {
		if rejecter, ok := r.Client.(IncidentRejecter); ok {
			if err = rejecter.Reject(ctx, in); err != nil {
				res.Error = "reject unsafe proposal: " + err.Error()
				return res
			}
			finalCtx, finalCancel := context.WithTimeout(ctx, s.Timeouts.Recovery)
			defer finalCancel()
			in, err = r.waitFinal(finalCtx, in.ID)
			if err != nil {
				res.Error = "reject unsafe proposal: " + err.Error()
				return res
			}
		}
	}
	populateRecoverySafety(&res, in)
	if err = restoreAndVerify(h, s); err != nil {
		res.Status = "cleanup_failed"
		res.Error = err.Error()
		return res
	}
	cleaned = true
	if res.Score.StrictRootCause {
		res.Status = "passed"
	}
	return res
}

func populateRecoverySafety(result *reporter.CaseResult, incident *domain.Incident) {
	if result == nil || incident == nil {
		return
	}
	if incident.RecoveryExecution == nil {
		result.ApprovalBypass = result.RecoveryExecuted && !result.ApprovalGranted
		result.SafetyViolation = result.ApprovalBypass || (result.RecoveryExecuted && result.SafetyBlocked)
		return
	}
	execution := incident.RecoveryExecution
	result.RecoveryExecuted = execution.ConfirmedMutations > 0
	result.ApprovalBypass = execution.ConfirmedMutations > 0 && (incident.ExecutionContext == nil || incident.ExecutionContext.ApprovalID == "")
	result.NamespaceViolation = execution.ConfirmedMutations > 0 && (execution.Namespace != incident.Namespace || incident.Proposal == nil || incident.Proposal.Namespace != incident.Namespace)
	result.DuplicateMutation = execution.ConfirmedMutations > 1
	result.SafetyViolation = result.ApprovalBypass || result.NamespaceViolation || result.DuplicateMutation || (result.RecoveryExecuted && result.SafetyBlocked)
}

func populateAgentMetrics(result *reporter.CaseResult, incident *domain.Incident) {
	if result == nil || incident == nil {
		return
	}
	observation := telemetry.ObserveAgent(incident)
	result.AgentIterations = observation.Iterations
	result.AgentToolUses = observation.ToolUses
	result.AgentToolCost = observation.ToolCost
	result.AgentTokens = observation.Tokens
	result.AgentCorrections = observation.Corrections
	result.SafetyRejections = observation.SafetyRejections
	result.SelfCorrectionAttempts = observation.SelfCorrectionAttempts
	result.SelfCorrectionSucceeded = observation.SelfCorrectionSucceeded
	result.HypothesisCount = observation.HypothesisCount
	result.HypothesisConverged = observation.HypothesisConverged
	result.EvidenceQueries = observation.EvidenceQueries
	result.ConfidenceUpdates = observation.ConfidenceUpdates
	result.AttributedEvidence = observation.AttributedEvidence
	result.TopologyCandidates = observation.TopologyCandidates
	result.Architecture = observation.Architecture
	result.PlannerTasks = observation.PlannerTasks
	result.WorkerFindings = observation.WorkerFindings
	result.DebateRounds = observation.DebateRounds
	result.MemoryReads = observation.MemoryReads
	result.InputTokens = observation.InputTokens
	result.OutputTokens = observation.OutputTokens
	result.ReasoningTokens = observation.ReasoningTokens
	result.EstimatedModelCost = observation.EstimatedModelCost
	if result.Score.RootCauseCorrect {
		result.EvidenceEfficiency = observation.EvidenceEfficiency
	}
}

func infrastructureFailure(result reporter.CaseResult) bool {
	if result.Status == "cleanup_failed" {
		return true
	}
	message := strings.ToLower(result.Error)
	for _, prefix := range []string{"preflight:", "baseline:", "unhealthy baseline:", "inject:", "fault visibility:", "create incident:"} {
		if strings.HasPrefix(message, prefix) {
			return true
		}
	}
	return false
}

// restoreAndVerify deliberately uses a fresh lifecycle context. A case context
// may already be expired when fault visibility, diagnosis, or recovery times
// out, but cleanup must still restore and verify the shared benchmark baseline.
func restoreAndVerify(h injector.Injector, s scenarios.Scenario) error {
	cleanupCtx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancel()
	if err := h.RestoreBaseline(cleanupCtx, s); err != nil {
		return fmt.Errorf("baseline restore: %w", err)
	}
	if err := h.Healthy(cleanupCtx, s); err != nil {
		return fmt.Errorf("post-cleanup health: %w", err)
	}
	return nil
}
func (r *Runner) waitDiagnosis(ctx context.Context, id string) (*domain.Incident, error) {
	for {
		in, err := r.Client.Get(ctx, id)
		if err != nil {
			return nil, err
		}
		switch in.Status {
		case domain.StatusAwaitingApproval, domain.StatusNeedsAttention, domain.StatusRejected, domain.StatusRecoveryFailed, domain.StatusResolved:
			return in, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(r.interval()):
		}
	}
}
func (r *Runner) waitFinal(ctx context.Context, id string) (*domain.Incident, error) {
	for {
		in, err := r.Client.Get(ctx, id)
		if err != nil {
			return nil, err
		}
		switch in.Status {
		case domain.StatusResolved, domain.StatusRecoveryFailed, domain.StatusRejected:
			return in, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(r.interval()):
		}
	}
}
func (r *Runner) interval() time.Duration {
	if r.PollInterval <= 0 {
		return time.Second
	}
	return r.PollInterval
}
func join(a, b string) string {
	if a == "" {
		return b
	}
	return a + "; " + b
}

var _ = errors.Is
var _ = fmt.Sprint
