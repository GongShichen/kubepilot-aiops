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
)

type IncidentClient interface {
	Create(context.Context, scenarios.Scenario) (*domain.Incident, error)
	Get(context.Context, string) (*domain.Incident, error)
	Approve(context.Context, *domain.Incident) error
}
type Runner struct {
	Registry        *injector.Registry
	Client          IncidentClient
	AutoApprove     bool
	PollInterval    time.Duration
	OnResult        func(reporter.CaseResult)
	DiagnosisMethod string
}

const cleanupTimeout = 2 * time.Minute

func (r *Runner) Run(ctx context.Context, items []scenarios.Scenario) []reporter.CaseResult {
	out := make([]reporter.CaseResult, 0, len(items))
	for _, s := range items {
		if ctx.Err() != nil {
			break
		}
		caseCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		result := r.runOne(caseCtx, s)
		cancel()
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
func (r *Runner) runOne(ctx context.Context, s scenarios.Scenario) (res reporter.CaseResult) {
	started := time.Now()
	res = reporter.CaseResult{CaseID: s.ID, Category: s.Category, Status: "failed", DiagnosisMethod: r.DiagnosisMethod}
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
	diagnosisCtx, diagnosisCancel := context.WithTimeout(ctx, s.Timeouts.Diagnosis)
	defer diagnosisCancel()
	in, err = r.waitDiagnosis(diagnosisCtx, in.ID)
	if err != nil {
		res.Error = "diagnosis: " + err.Error()
		return res
	}
	res.Score = scorer.Incident(s, in)
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
		if err = r.Client.Approve(ctx, in); err != nil {
			res.Error = "approve: " + err.Error()
			return res
		}
		recoveryCtx, recoveryCancel := context.WithTimeout(ctx, s.Timeouts.Recovery)
		defer recoveryCancel()
		in, err = r.waitFinal(recoveryCtx, in.ID)
		if err != nil {
			res.Error = "recovery: " + err.Error()
			return res
		}
		_ = in
	}
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

func populateAgentMetrics(result *reporter.CaseResult, incident *domain.Incident) {
	if result == nil || incident == nil {
		return
	}
	if incident.AgentBudget != nil {
		result.AgentToolUses = incident.AgentBudget.IncidentUses
		result.AgentToolCost = incident.AgentBudget.IncidentCost
		result.AgentTokens = incident.AgentBudget.IncidentTokens
		for _, usage := range incident.AgentBudget.Usage {
			result.AgentCorrections += usage.Corrections
		}
	}
	if incident.DiagnosisLedger != nil {
		for _, feedback := range incident.DiagnosisLedger.SafetyFeedback {
			if !feedback.Allowed {
				result.SafetyRejections++
			}
		}
		result.HypothesisCount = len(incident.DiagnosisLedger.Drafts)
		result.HypothesisConverged = hypothesisConverged(incident.DiagnosisLedger)
		for _, decision := range incident.DiagnosisLedger.AgentDecisions {
			if isEvidenceQuery(decision.SelectedAction) {
				result.EvidenceQueries++
			}
		}
	}
	result.SelfCorrectionAttempts = result.AgentCorrections
	result.SelfCorrectionSucceeded = result.SelfCorrectionAttempts > 0 && result.HypothesisConverged
	if result.EvidenceQueries > 0 && result.Score.RootCauseCorrect {
		result.EvidenceEfficiency = 1 / float64(result.EvidenceQueries)
	}
	for _, evidence := range incident.Evidence {
		if evidence.Attribution != nil {
			result.AttributedEvidence++
		}
	}
	if incident.DiagnosisLedger != nil {
		for _, hypothesis := range incident.DiagnosisLedger.Verified {
			result.ConfidenceUpdates += len(hypothesis.ConfidenceHistory)
		}
		for _, candidate := range incident.DiagnosisLedger.Candidates {
			if candidate.Rank.TopologySimilarity > 0 || candidate.SourceRanks["topology"] > 0 {
				result.TopologyCandidates++
			}
		}
	}
}

func hypothesisConverged(ledger *domain.DiagnosisLedger) bool {
	if ledger == nil || ledger.SelectedHypothesisID == "" {
		return false
	}
	for _, verified := range ledger.Verified {
		if verified.Draft.ID == ledger.SelectedHypothesisID && verified.Status == domain.HypothesisAccepted && len(verified.ConfidenceHistory) > 0 {
			return true
		}
	}
	return false
}

func isEvidenceQuery(action string) bool {
	return strings.HasPrefix(action, "query_") || strings.HasPrefix(action, "retrieve_") || strings.HasPrefix(action, "load_") || strings.Contains(action, "evidence")
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
