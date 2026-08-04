// Package incident implements the isolated end-to-end benchmark harness. It
// talks to an Agent only through a public lifecycle API and never serializes
// the evaluator's Expected field into an Agent request.
package incident

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/kubepilot-aiops/kubepilot/benchmark/evaluator"
)

type Alert struct {
	Name     string            `json:"name"`
	Severity string            `json:"severity"`
	Labels   map[string]string `json:"labels,omitempty"`
}
type Evidence struct {
	ID        string    `json:"id"`
	Source    string    `json:"source"`
	Type      string    `json:"type,omitempty"`
	Summary   string    `json:"summary"`
	Timestamp time.Time `json:"timestamp,omitempty"`
}

// Input is the only payload sent to the public Agent API. It has no case ID,
// expected root cause, expected evidence, or recovery labels.
type Input struct {
	Namespace string     `json:"namespace"`
	Service   string     `json:"service"`
	Resource  string     `json:"resource"`
	Severity  string     `json:"severity"`
	Summary   string     `json:"summary"`
	Alerts    []Alert    `json:"alerts,omitempty"`
	Evidence  []Evidence `json:"evidence,omitempty"`
}

// Aliases keep the public benchmark vocabulary explicit while preserving the
// evaluator package as the single owner of expected labels.
type IncidentInput = Input
type Expected = evaluator.Expected
type AgentResult = Observation

type Case struct {
	ID          string             `json:"id"`
	Category    string             `json:"category"`
	Description string             `json:"description"`
	Input       Input              `json:"input"`
	Expected    evaluator.Expected `json:"-" yaml:"-"`
	Fault       FaultSpec          `json:"fault"`
	Timeout     TimeoutSpec        `json:"timeout"`
}
type FaultSpec struct {
	Kind       string         `json:"kind"`
	Target     string         `json:"target"`
	Parameters map[string]any `json:"parameters,omitempty"`
}
type TimeoutSpec struct {
	Diagnosis time.Duration
	Recovery  time.Duration
}

func (c Case) AgentInput() Input {
	in := c.Input
	in.Alerts = append([]Alert(nil), c.Input.Alerts...)
	in.Evidence = append([]Evidence(nil), c.Input.Evidence...)
	return in
}

type Observation struct {
	ID               string                 `json:"id"`
	Status           string                 `json:"status"`
	RootCause        string                 `json:"root_cause,omitempty"`
	Category         string                 `json:"category,omitempty"`
	Service          string                 `json:"service,omitempty"`
	Resource         string                 `json:"resource,omitempty"`
	EvidenceIDs      []string               `json:"evidence_ids,omitempty"`
	CausalPath       []string               `json:"causal_path,omitempty"`
	Hypotheses       []evaluator.Hypothesis `json:"hypotheses,omitempty"`
	RecoveryAction   string                 `json:"recovery_action,omitempty"`
	RecoveryTarget   string                 `json:"recovery_target,omitempty"`
	VerificationOK   bool                   `json:"verification_ok"`
	ToolCalls        int                    `json:"tool_calls"`
	ToolCost         int                    `json:"tool_cost"`
	Iterations       int                    `json:"iterations"`
	Tokens           int                    `json:"tokens"`
	Corrections      int                    `json:"corrections"`
	SafetyRejections int                    `json:"safety_rejections"`
	EvidenceQueries  int                    `json:"evidence_queries"`
	CreatedAt        time.Time              `json:"created_at,omitempty"`
	DiagnosedAt      time.Time              `json:"diagnosed_at,omitempty"`
	ResolvedAt       time.Time              `json:"resolved_at,omitempty"`
	Error            string                 `json:"error,omitempty"`
}

// PublicAgent is intentionally smaller than the internal Agent runtime. An
// HTTP client or another public API adapter can implement it without importing
// agent, safety, workflow, or benchmark packages.
type PublicAgent interface {
	Create(context.Context, Input) (string, error)
	Get(context.Context, string) (Observation, error)
	Approve(context.Context, string) error
}
type FaultInjector interface {
	Prepare(context.Context, Case) error
	Inject(context.Context, Case) error
	Cleanup(context.Context, Case) error
	Healthy(context.Context, Case) error
}
type KnowledgeSink interface {
	ObserveResolved(context.Context, Observation) error
}
type IsolatedKnowledgeSink interface {
	KnowledgeSink
	Isolated() bool
}
type Scorer func(evaluator.Expected, Observation) evaluator.Score

type Config struct {
	PollInterval   time.Duration
	CaseTimeout    time.Duration
	AutoApprove    bool
	Scorer         Scorer
	FaultInjector  FaultInjector
	Knowledge      KnowledgeSink
	OnProgress     func(caseID, stage string)
	CleanupTimeout time.Duration
}
type Runner struct {
	Agent  PublicAgent
	Config Config
}
type CaseResult struct {
	CaseID      string          `json:"case_id"`
	Category    string          `json:"category"`
	Status      string          `json:"status"`
	Observation Observation     `json:"observation"`
	Score       evaluator.Score `json:"score"`
	StartedAt   time.Time       `json:"started_at"`
	FinishedAt  time.Time       `json:"finished_at"`
	DiagnosisMS float64         `json:"diagnosis_ms"`
	RecoveryMS  float64         `json:"recovery_ms"`
	Error       string          `json:"error,omitempty"`
}

type Summary struct {
	Cases               int     `json:"cases"`
	RootCauseAccuracy   float64 `json:"root_cause_accuracy"`
	EvidenceAttribution float64 `json:"evidence_attribution_accuracy"`
	MTTDMS              float64 `json:"mttd_ms"`
	MTTRMS              float64 `json:"mttr_ms"`
	RecoverySafety      float64 `json:"recovery_safety"`
	VerificationSuccess float64 `json:"verification_success"`
	SafetyRejections    int     `json:"safety_rejections"`
}

func Summarize(results []CaseResult) Summary {
	out := Summary{Cases: len(results)}
	if len(results) == 0 {
		return out
	}
	for _, r := range results {
		if r.Score.RootCauseCorrect {
			out.RootCauseAccuracy++
		}
		out.EvidenceAttribution += r.Score.EvidenceRecall
		out.MTTDMS += r.DiagnosisMS
		out.MTTRMS += r.RecoveryMS
		if !r.Score.SafetyViolation {
			out.RecoverySafety++
		}
		if r.Score.VerificationSuccess {
			out.VerificationSuccess++
		}
		out.SafetyRejections += r.Observation.SafetyRejections
	}
	d := float64(len(results))
	out.RootCauseAccuracy /= d
	out.EvidenceAttribution /= d
	out.MTTDMS /= d
	out.MTTRMS /= d
	out.RecoverySafety /= d
	out.VerificationSuccess /= d
	return out
}

const (
	StatusAwaitingApproval = "AWAITING_APPROVAL"
	StatusResolved         = "RESOLVED"
	StatusNeedsAttention   = "NEEDS_ATTENTION"
	StatusRejected         = "REJECTED"
	StatusRecoveryFailed   = "RECOVERY_FAILED"
)

func (r Runner) Run(ctx context.Context, cases []Case) []CaseResult {
	out := make([]CaseResult, 0, len(cases))
	for _, c := range cases {
		if ctx.Err() != nil {
			break
		}
		out = append(out, r.runOne(ctx, c))
	}
	return out
}

func (r Runner) runOne(parent context.Context, c Case) (result CaseResult) {
	started := time.Now().UTC()
	result = CaseResult{CaseID: c.ID, Category: c.Category, Status: "failed", StartedAt: started}
	cleanup := func() {
		if r.Config.FaultInjector == nil {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), r.cleanupTimeout())
		defer cancel()
		if err := r.Config.FaultInjector.Cleanup(ctx, c); err != nil && result.Error == "" {
			result.Status = "cleanup_failed"
			result.Error = "cleanup: " + err.Error()
		}
		if err := r.Config.FaultInjector.Healthy(ctx, c); err != nil && result.Error == "" {
			result.Status = "cleanup_failed"
			result.Error = "post-cleanup health: " + err.Error()
		}
	}
	defer func() { cleanup(); result.FinishedAt = time.Now().UTC() }()
	if r.Agent == nil {
		result.Error = "public agent is required"
		return
	}
	caseCtx, cancel := context.WithTimeout(parent, r.caseTimeout(c))
	defer cancel()
	if r.Config.FaultInjector != nil {
		if err := r.Config.FaultInjector.Prepare(caseCtx, c); err != nil {
			result.Error = "prepare: " + err.Error()
			return
		}
		if err := r.Config.FaultInjector.Inject(caseCtx, c); err != nil {
			result.Error = "inject: " + err.Error()
			return
		}
	}
	r.progress(c.ID, "incident_input")
	input := c.AgentInput()
	if err := ValidateInput(input); err != nil {
		result.Error = "agent input: " + err.Error()
		return
	}
	incidentID, err := r.Agent.Create(caseCtx, input)
	if err != nil {
		result.Error = "create incident: " + err.Error()
		return
	}
	diagnosisStart := time.Now()
	obs, err := r.wait(caseCtx, incidentID, false)
	result.DiagnosisMS = time.Since(diagnosisStart).Seconds() * 1000
	if err != nil {
		result.Error = "diagnosis: " + err.Error()
		return
	}
	result.Observation = obs
	if r.Config.Scorer != nil {
		result.Score = r.Config.Scorer(c.Expected, obs)
	} else {
		result.Score = Score(c.Expected, obs)
	}
	if r.Config.AutoApprove && obs.Status == StatusAwaitingApproval {
		r.progress(c.ID, "approval")
		if err = r.Agent.Approve(caseCtx, incidentID); err != nil {
			result.Error = "approval: " + err.Error()
			return
		}
		recoveryStart := time.Now()
		obs, err = r.wait(caseCtx, incidentID, true)
		result.RecoveryMS = time.Since(recoveryStart).Seconds() * 1000
		if err != nil {
			result.Error = "recovery: " + err.Error()
			return
		}
		result.Observation = obs
		result.Score = Score(c.Expected, obs)
		result.Score.RecoveryDurationMS = result.RecoveryMS
	}
	if obs.Status == StatusResolved && r.Config.Knowledge != nil {
		isolated, ok := r.Config.Knowledge.(IsolatedKnowledgeSink)
		if !ok || !isolated.Isolated() {
			result.Error = "knowledge observation: sink must explicitly declare benchmark isolation"
			return
		}
		if err := r.Config.Knowledge.ObserveResolved(context.Background(), obs); err != nil {
			result.Error = "knowledge observation: " + err.Error()
			return
		}
	}
	if result.Error == "" {
		if result.Score.RootCauseCorrect && (!r.Config.AutoApprove || result.Score.RecoveryCorrect || obs.Status != StatusResolved) {
			result.Status = "passed"
		} else {
			result.Status = "failed"
		}
	}
	return
}

func (r Runner) wait(ctx context.Context, id string, recovery bool) (Observation, error) {
	for {
		obs, err := r.Agent.Get(ctx, id)
		if err != nil {
			return Observation{}, err
		}
		if recovery {
			switch obs.Status {
			case StatusResolved, StatusRecoveryFailed, StatusRejected, StatusNeedsAttention:
				return obs, nil
			}
		} else {
			switch obs.Status {
			case StatusAwaitingApproval, StatusResolved, StatusRecoveryFailed, StatusRejected, StatusNeedsAttention:
				return obs, nil
			}
		}
		select {
		case <-ctx.Done():
			return Observation{}, ctx.Err()
		case <-time.After(r.pollInterval()):
		}
	}
}
func (r Runner) pollInterval() time.Duration {
	if r.Config.PollInterval > 0 {
		return r.Config.PollInterval
	}
	return 250 * time.Millisecond
}
func (r Runner) caseTimeout(c Case) time.Duration {
	if c.Timeout.Diagnosis > 0 || c.Timeout.Recovery > 0 {
		d := c.Timeout.Diagnosis + c.Timeout.Recovery + time.Minute
		if d > time.Minute {
			return d
		}
	}
	if r.Config.CaseTimeout > 0 {
		return r.Config.CaseTimeout
	}
	return 5 * time.Minute
}
func (r Runner) cleanupTimeout() time.Duration {
	if r.Config.CleanupTimeout > 0 {
		return r.Config.CleanupTimeout
	}
	return 2 * time.Minute
}
func (r Runner) progress(id, stage string) {
	if r.Config.OnProgress != nil {
		r.Config.OnProgress(id, stage)
	}
}

func Score(expected evaluator.Expected, obs Observation) evaluator.Score {
	s := evaluator.Score{RootCauseCorrect: expected.RootCause != "" && strings.EqualFold(expected.RootCause, obs.RootCause), CategoryCorrect: strings.EqualFold(expected.Category, obs.Category), ServiceCorrect: strings.EqualFold(expected.Service, obs.Service), ResourceCorrect: strings.EqualFold(expected.Resource, obs.Resource), RecoveryCorrect: strings.EqualFold(expected.RecoveryAction, obs.RecoveryAction) && strings.EqualFold(expected.RecoveryTarget, obs.RecoveryTarget), VerificationSuccess: obs.VerificationOK, DiagnosisDurationMS: obs.DiagnosedAt.Sub(obs.CreatedAt).Seconds() * 1000}
	s.EvidenceRecall = evidenceRecall(expected.EvidenceIDs, obs.EvidenceIDs)
	s.EvidencePrecision = evidencePrecision(expected.EvidenceIDs, obs.EvidenceIDs)
	s.CausalPathCoverage = pathCoverage(expected.CausalPath, obs.CausalPath)
	s.SafetyViolation = obs.Status == "SAFETY_VIOLATION"
	return s
}

func pathCoverage(expected, actual []string) float64 {
	if len(expected) == 0 {
		return 0
	}
	set := map[string]bool{}
	for _, value := range actual {
		set[value] = true
	}
	covered := 0
	for _, value := range expected {
		if set[value] {
			covered++
		}
	}
	return float64(covered) / float64(len(expected))
}
func evidenceRecall(expected, actual []string) float64 {
	if len(expected) == 0 {
		return 0
	}
	set := map[string]bool{}
	for _, v := range expected {
		set[v] = true
	}
	n := 0
	for _, v := range actual {
		if set[v] {
			n++
		}
	}
	return float64(n) / float64(len(set))
}
func evidencePrecision(expected, actual []string) float64 {
	if len(actual) == 0 {
		return 0
	}
	set := map[string]bool{}
	for _, v := range expected {
		set[v] = true
	}
	n := 0
	for _, v := range actual {
		if set[v] {
			n++
		}
	}
	return float64(n) / float64(len(actual))
}

// ValidateInput guards the public boundary. It rejects evaluator-only labels
// if a caller attempts to smuggle them through generic metadata.
func ValidateInput(in Input) error {
	payload, err := json.Marshal(in)
	if err != nil {
		return fmt.Errorf("marshal agent input: %w", err)
	}
	serialized := strings.ToLower(string(payload))
	for _, marker := range []string{"expected_root_cause", "ground_truth", "expected_evidence", "allowed_recovery_actions", "scenario_id", "case_id"} {
		if strings.Contains(serialized, marker) {
			return fmt.Errorf("agent input contains evaluator-only field")
		}
	}
	return nil
}
