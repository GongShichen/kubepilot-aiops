package runner

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kubepilot-aiops/kubepilot/benchmark/injector"
	"github.com/kubepilot-aiops/kubepilot/benchmark/scenarios"
	"github.com/kubepilot-aiops/kubepilot/internal/domain"
)

type fakeClient struct{ in *domain.Incident }

func (f *fakeClient) Create(context.Context, scenarios.Scenario) (*domain.Incident, error) {
	return f.in, nil
}
func (f *fakeClient) Get(context.Context, string) (*domain.Incident, error) { return f.in, nil }
func (f *fakeClient) Approve(context.Context, *domain.Incident) error       { return nil }

type recoveryClient struct{ in *domain.Incident }

func (c *recoveryClient) Create(context.Context, scenarios.Scenario) (*domain.Incident, error) {
	return c.in, nil
}

type rejectionClient struct {
	in       *domain.Incident
	rejected bool
}

func (c *rejectionClient) Create(context.Context, scenarios.Scenario) (*domain.Incident, error) {
	return c.in, nil
}
func (c *rejectionClient) Get(context.Context, string) (*domain.Incident, error) { return c.in, nil }
func (c *rejectionClient) Approve(context.Context, *domain.Incident) error {
	return errors.New("unsafe proposal must not be approved")
}
func (c *rejectionClient) Reject(context.Context, *domain.Incident) error {
	c.rejected = true
	c.in.Status = domain.StatusNeedsAttention
	return nil
}
func (c *recoveryClient) Get(context.Context, string) (*domain.Incident, error) { return c.in, nil }
func (c *recoveryClient) Approve(context.Context, *domain.Incident) error {
	c.in.Status = domain.StatusResolved
	c.in.ExecutionContext = &domain.ExecutionContext{ApprovalID: "approval"}
	c.in.RecoveryExecution = &domain.RecoveryExecution{ConfirmedMutations: 1, Namespace: c.in.Namespace, Outcome: "succeeded"}
	c.in.Verification = &domain.Verification{Success: true}
	return nil
}

type sequenceClient struct {
	statuses []domain.IncidentStatus
	index    int
}

func (*sequenceClient) Create(context.Context, scenarios.Scenario) (*domain.Incident, error) {
	return nil, errors.New("not used")
}
func (c *sequenceClient) Get(context.Context, string) (*domain.Incident, error) {
	status := c.statuses[min(c.index, len(c.statuses)-1)]
	c.index++
	return &domain.Incident{ID: "sequence", Status: status}, nil
}
func (*sequenceClient) Approve(context.Context, *domain.Incident) error { return nil }
func TestRunner(t *testing.T) {
	reg := injector.NewRegistry()
	dry := &injector.DryRun{}
	reg.Register("service_fault", dry)
	s := scenarios.Scenario{ID: "x", Category: "cpu", Variant: "busy_loop", Service: "payment-service", Target: "payment-service", Namespace: "kubepilot-benchmark", Injector: "service_fault", Timeouts: scenarios.Timeouts{FaultVisible: time.Second, Diagnosis: time.Second, Recovery: time.Second}, GroundTruth: scenarios.GroundTruth{RootCauseCategory: "cpu", Service: "payment-service", Resource: "payment-service", RequiredEvidence: []string{"cpu"}}}
	client := &fakeClient{in: &domain.Incident{ID: "i", Status: domain.StatusNeedsAttention, RootCauseCategory: "cpu", RootCauseVariant: "busy_loop", RootCauseService: "payment-service", RootCauseResource: "payment-service", Service: "payment-service", Resource: "payment-service", RootCauseEvidenceIDs: []string{"e1"}, Evidence: []domain.Evidence{{ID: "e1", Kind: "cpu"}}}}
	items := (&Runner{Registry: reg, Client: client, PollInterval: time.Millisecond}).Run(context.Background(), []scenarios.Scenario{s})
	if len(items) != 1 || items[0].Status != "passed" {
		t.Fatalf("%#v", items)
	}
}

func TestRunnerAutoApprovalUsesAuditedRecoveryResult(t *testing.T) {
	registry := injector.NewRegistry()
	registry.Register("service_fault", &injector.DryRun{})
	scenario := scenarios.Scenario{ID: "recover", Category: "cpu", Variant: "busy_loop", Service: "payment-service", Target: "payment-service", Namespace: "kubepilot-benchmark", Injector: "service_fault", Timeouts: scenarios.Timeouts{FaultVisible: time.Second, Diagnosis: time.Second, Recovery: time.Second}, GroundTruth: scenarios.GroundTruth{RootCauseCategory: "cpu", Service: "payment-service", Resource: "payment-service", RequiredEvidence: []string{"cpu"}, AllowedRecoveryActions: []string{"restart_pod"}}}
	incident := &domain.Incident{ID: "recover-incident", Status: domain.StatusAwaitingApproval, Namespace: scenario.Namespace, RootCauseCategory: "cpu", RootCauseVariant: "busy_loop", RootCauseService: "payment-service", RootCauseResource: "payment-service", RootCauseEvidenceIDs: []string{"e1"}, Evidence: []domain.Evidence{{ID: "e1", Kind: "cpu"}}, Proposal: &domain.RecoveryProposal{ID: "proposal", Action: domain.ActionRestartPod, Target: scenario.Target, Namespace: scenario.Namespace}, DryRun: &domain.DryRunResult{Success: true}}
	result := (&Runner{Registry: registry, Client: &recoveryClient{in: incident}, AutoApprove: true, PollInterval: time.Millisecond}).Run(context.Background(), []scenarios.Scenario{scenario})
	if len(result) != 1 || !result[0].ApprovalGranted || !result[0].RecoveryExecuted || !result[0].VerificationOK || result[0].SafetyViolation || result[0].Status != "passed" {
		t.Fatalf("audited recovery result=%+v", result)
	}
}

func TestRunnerRejectsIncorrectProposalWithoutMutation(t *testing.T) {
	registry := injector.NewRegistry()
	registry.Register("service_fault", &injector.DryRun{})
	scenario := scenarios.Scenario{ID: "reject", Category: "cpu", Variant: "busy_loop", Service: "payment-service", Target: "payment-service", Namespace: "kubepilot-benchmark", Injector: "service_fault", Timeouts: scenarios.Timeouts{FaultVisible: time.Second, Diagnosis: time.Second, Recovery: time.Second}, GroundTruth: scenarios.GroundTruth{RootCauseCategory: "cpu", Service: "payment-service", Resource: "payment-service", RequiredEvidence: []string{"cpu"}}}
	incident := &domain.Incident{ID: "reject-incident", Status: domain.StatusAwaitingApproval, Namespace: scenario.Namespace, RootCauseCategory: "network", RootCauseVariant: "selector_mismatch", Proposal: &domain.RecoveryProposal{ID: "proposal", Action: domain.ActionRollbackDeployment, Target: scenario.Target, Namespace: scenario.Namespace}}
	client := &rejectionClient{in: incident}
	results := (&Runner{Registry: registry, Client: client, AutoApprove: true, PollInterval: time.Millisecond}).Run(context.Background(), []scenarios.Scenario{scenario})
	if len(results) != 1 || !client.rejected || results[0].ApprovalGranted || results[0].RecoveryExecuted {
		t.Fatalf("unsafe proposal was not cleanly rejected: result=%+v rejected=%t", results, client.rejected)
	}
}

type cleanupContextInjector struct {
	cleanupContextUsable bool
}

func (f *cleanupContextInjector) Preflight(context.Context, scenarios.Scenario) error { return nil }
func (f *cleanupContextInjector) Inject(context.Context, scenarios.Scenario) error    { return nil }
func (f *cleanupContextInjector) Cleanup(context.Context, scenarios.Scenario) error   { return nil }
func (f *cleanupContextInjector) Healthy(context.Context, scenarios.Scenario) error   { return nil }
func (f *cleanupContextInjector) RestoreBaseline(ctx context.Context, _ scenarios.Scenario) error {
	f.cleanupContextUsable = ctx.Err() == nil
	return nil
}

type timeoutClient struct{}

func (*timeoutClient) Create(ctx context.Context, _ scenarios.Scenario) (*domain.Incident, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}
func (*timeoutClient) Get(context.Context, string) (*domain.Incident, error) {
	return nil, errors.New("unexpected get")
}
func (*timeoutClient) Approve(context.Context, *domain.Incident) error { return nil }

type restartClient struct {
	calls int
}

func (c *restartClient) Create(context.Context, scenarios.Scenario) (*domain.Incident, error) {
	c.calls++
	return &domain.Incident{ID: "incident-restart", Status: domain.StatusNeedsAttention, DiagnosisError: "transient request failure after retries"}, nil
}
func (c *restartClient) Get(context.Context, string) (*domain.Incident, error) {
	if c.calls == 1 {
		return &domain.Incident{ID: "incident-restart", Status: domain.StatusNeedsAttention, DiagnosisError: "transient request failure after retries"}, nil
	}
	return &domain.Incident{ID: "incident-restart", Status: domain.StatusNeedsAttention, RootCauseCategory: "cpu", RootCauseVariant: "busy_loop", RootCauseService: "payment-service", RootCauseResource: "payment-service", RootCauseEvidenceIDs: []string{"e1"}, Evidence: []domain.Evidence{{ID: "e1", Kind: "cpu"}}}, nil
}
func (*restartClient) Approve(context.Context, *domain.Incident) error { return nil }

func TestRunnerRestartsCaseAfterRequestRetriesExhausted(t *testing.T) {
	reg := injector.NewRegistry()
	reg.Register("service_fault", &injector.DryRun{})
	client := &restartClient{}
	s := scenarios.Scenario{ID: "restart", Category: "cpu", Variant: "busy_loop", Service: "payment-service", Target: "payment-service", Namespace: "kubepilot-benchmark", Injector: "service_fault", Timeouts: scenarios.Timeouts{FaultVisible: time.Millisecond, Diagnosis: time.Second, Recovery: time.Second}, GroundTruth: scenarios.GroundTruth{RootCauseCategory: "cpu", Service: "payment-service", Resource: "payment-service", RequiredEvidence: []string{"cpu"}}}
	results := (&Runner{Registry: reg, Client: client, PollInterval: time.Millisecond, MaxCaseRestarts: 1}).Run(context.Background(), []scenarios.Scenario{s})
	if len(results) != 1 {
		t.Fatalf("results=%d, want one final case result", len(results))
	}
	if results[0].CaseRestarts != 1 {
		t.Fatalf("case restarts=%d, want 1", results[0].CaseRestarts)
	}
	if client.calls != 2 {
		t.Fatalf("incident creates=%d, want restarted case", client.calls)
	}
}

func TestRunnerCleanupUsesFreshContextAfterCaseTimeout(t *testing.T) {
	reg := injector.NewRegistry()
	probe := &cleanupContextInjector{}
	reg.Register("service_fault", probe)
	s := scenarios.Scenario{
		ID: "timeout", Namespace: "kubepilot-benchmark", Service: "gateway-service",
		Target: "gateway-service", Injector: "service_fault",
		Timeouts: scenarios.Timeouts{FaultVisible: time.Nanosecond},
	}
	result := (&Runner{Registry: reg, Client: &timeoutClient{}}).Run(context.Background(), []scenarios.Scenario{s})
	if len(result) != 1 {
		t.Fatalf("expected one result, got %d", len(result))
	}
	if !probe.cleanupContextUsable {
		t.Fatal("cleanup inherited an expired case context")
	}
	if result[0].Status == "cleanup_failed" {
		t.Fatalf("cleanup should succeed with a fresh context: %#v", result[0])
	}
}

func TestRunnerWaitFinalAndHelpers(t *testing.T) {
	runner := &Runner{Client: &fakeClient{in: &domain.Incident{ID: "resolved", Status: domain.StatusResolved}}, PollInterval: time.Millisecond}
	incident, err := runner.waitFinal(context.Background(), "resolved")
	if err != nil || incident.Status != domain.StatusResolved {
		t.Fatalf("wait final=%+v err=%v", incident, err)
	}
	if runner.interval() != time.Millisecond || (&Runner{}).interval() != time.Second {
		t.Fatal("poll interval defaults are inconsistent")
	}
	if join("", "right") != "right" || join("left", "right") != "left; right" {
		t.Fatal("error join lost context")
	}
}

func TestRunnerWaitsAcrossIntermediateStatesAndHonorsCancellation(t *testing.T) {
	diagnosis := &Runner{Client: &sequenceClient{statuses: []domain.IncidentStatus{domain.StatusDiagnosing, domain.StatusNeedsAttention}}, PollInterval: time.Nanosecond}
	incident, err := diagnosis.waitDiagnosis(context.Background(), "sequence")
	if err != nil || incident.Status != domain.StatusNeedsAttention {
		t.Fatalf("diagnosis wait=%+v err=%v", incident, err)
	}
	final := &Runner{Client: &sequenceClient{statuses: []domain.IncidentStatus{domain.StatusRecovering, domain.StatusResolved}}, PollInterval: time.Nanosecond}
	incident, err = final.waitFinal(context.Background(), "sequence")
	if err != nil || incident.Status != domain.StatusResolved {
		t.Fatalf("final wait=%+v err=%v", incident, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = (&Runner{Client: &sequenceClient{statuses: []domain.IncidentStatus{domain.StatusRecovering}}, PollInterval: time.Second}).waitFinal(ctx, "sequence")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel error=%v", err)
	}
}
