package agent

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/cloudwego/eino/components/tool"
	"github.com/kubepilot-aiops/kubepilot/internal/domain"
	"github.com/kubepilot-aiops/kubepilot/internal/safety"
)

func TestRecoveryRejectsAnotherNamespace(t *testing.T) {
	if _, err := canonicalProposalTarget("other/gateway-service", "kubepilot-benchmark", "gateway-service"); err == nil {
		t.Fatal("expected cross-namespace target to be rejected")
	}
}

type recoveryTestClient struct {
	restarts   int
	uid        string
	rv         string
	restartErr error
}

func (c *recoveryTestClient) RestartDeployment(context.Context, string, string) error {
	c.restarts++
	return c.restartErr
}

func TestMutationAPIFailureIsTreatedAsUnknownAndNotReplayable(t *testing.T) {
	client := &recoveryTestClient{restartErr: errors.New("transport failure")}
	executor := KubernetesExecutor{Client: client, Guard: &recoveryTestGuard{}}
	incident := &domain.Incident{ID: "incident-unknown", ExecutionContext: &domain.ExecutionContext{IdempotencyKey: "approval-key"}}
	proposal := domain.RecoveryProposal{Action: domain.ActionRestartPod, Namespace: "kubepilot-demo", Target: "gateway-service", TargetUID: "uid-1", ResourceVersion: "rv-1", Parameters: map[string]any{}}
	if err := executor.Execute(context.Background(), incident, proposal); !errors.Is(err, ErrActionResultUnknown) {
		t.Fatalf("mutation transport error was not classified as unknown: %v", err)
	}
	if err := executor.Execute(context.Background(), incident, proposal); !errors.Is(err, ErrActionResultUnknown) {
		t.Fatalf("unknown action was replayed: %v", err)
	}
	if client.restarts != 1 {
		t.Fatalf("unknown mutation was attempted %d times", client.restarts)
	}
}
func (*recoveryTestClient) ScaleDeployment(context.Context, string, string, int32) error {
	return nil
}
func (*recoveryTestClient) RollbackDeployment(context.Context, string, string) error { return nil }
func (c *recoveryTestClient) TargetState(context.Context, string, string) (string, string, bool, int32, error) {
	uid, rv := c.uid, c.rv
	if uid == "" {
		uid = "uid-1"
	}
	if rv == "" {
		rv = "rv-1"
	}
	return uid, rv, true, 0, nil
}

type recoveryTestGuard struct{ claimed bool }

func (g *recoveryTestGuard) ClaimAction(context.Context, string, time.Duration) (bool, error) {
	if g.claimed {
		return false, nil
	}
	g.claimed = true
	return true, nil
}
func (*recoveryTestGuard) CompleteAction(context.Context, string, string, time.Duration) error {
	return nil
}

func TestKubernetesActionCannotBeReplayed(t *testing.T) {
	client := &recoveryTestClient{}
	executor := KubernetesExecutor{Client: client, Guard: &recoveryTestGuard{}}
	incident := &domain.Incident{
		ID: "incident-1",
		ExecutionContext: &domain.ExecutionContext{
			IdempotencyKey: "approval-key-1",
		},
	}
	proposal := domain.RecoveryProposal{
		Action:          domain.ActionRestartPod,
		Namespace:       "kubepilot-demo",
		Target:          "gateway-service",
		TargetUID:       "uid-1",
		ResourceVersion: "rv-1",
		Parameters:      map[string]any{},
	}
	if err := executor.Execute(context.Background(), incident, proposal); err != nil {
		t.Fatal(err)
	}
	if err := executor.Execute(context.Background(), incident, proposal); !errors.Is(err, ErrActionResultUnknown) {
		t.Fatalf("expected unknown-result replay guard, got %v", err)
	}
	if client.restarts != 1 {
		t.Fatalf("recovery mutation executed %d times", client.restarts)
	}
}

func TestDryRunFreshnessDetectsTargetDrift(t *testing.T) {
	client := &recoveryTestClient{}
	executor := KubernetesExecutor{Client: client}
	proposal := &domain.RecoveryProposal{Action: domain.ActionRestartPod, Namespace: "kubepilot-demo", Target: "gateway-service", TargetUID: "uid-1", ResourceVersion: "rv-1", Parameters: map[string]any{"restarted_at": "fixed"}}
	dryRun := &domain.DryRunResult{Success: true, Action: proposal.Action, Target: proposal.Target, After: map[string]any{"annotation": "fixed"}, ValidatedAt: time.Now().UTC()}
	dryRun.MutationSpecHash = proposalMutationHash(proposal, dryRun.After)
	if err := executor.ValidateDryRunFreshness(context.Background(), proposal, dryRun); err != nil {
		t.Fatal(err)
	}
	client.rv = "rv-changed"
	if err := executor.ValidateDryRunFreshness(context.Background(), proposal, dryRun); err == nil {
		t.Fatal("resourceVersion drift was not rejected")
	}
}

type sequenceVerificationExecutor struct {
	samples []bool
	index   int
}

func (*sequenceVerificationExecutor) Execute(context.Context, *domain.Incident, domain.RecoveryProposal) error {
	return nil
}

func (e *sequenceVerificationExecutor) Verify(context.Context, *domain.Incident) (domain.Verification, error) {
	index := e.index
	if index >= len(e.samples) {
		index = len(e.samples) - 1
	}
	e.index++
	success := e.samples[index]
	restarts := int32(0)
	return domain.Verification{Success: success, Checks: map[string]bool{"pod_ready": success}, RestartCount: &restarts}, nil
}

func TestVerificationControllerRequiresThreeConsecutiveRounds(t *testing.T) {
	executor := &sequenceVerificationExecutor{samples: []bool{true, false, true, true, true}}
	state := &WorkflowState{Incident: &domain.Incident{ID: "verification", Status: domain.StatusVerifying}, VerificationState: VerificationState{StartedAt: time.Now().UTC()}}
	transition := func(_ context.Context, incident *domain.Incident, status domain.IncidentStatus) error {
		return domain.Transition(incident, status)
	}
	result, err := runVerificationController(context.Background(), state, SupervisorDeps{Executor: executor, VerificationInterval: time.Millisecond, VerificationTimeout: time.Second}, transition)
	if err != nil {
		t.Fatal(err)
	}
	if result.Incident.Status != domain.StatusResolved || result.VerificationState.Attempts != 5 || result.VerificationState.ConsecutiveSuccess != 3 {
		t.Fatalf("verification did not require a consecutive streak: status=%s state=%+v", result.Incident.Status, result.VerificationState)
	}
}

func TestRecoveryNamespaceOverrideReturnsFatalSafetyFeedback(t *testing.T) {
	tools, err := buildConstrainedRecoveryTools(constrainedToolDeps{})
	if err != nil {
		t.Fatal(err)
	}
	var submit tool.InvokableTool
	for _, candidate := range tools {
		info, infoErr := candidate.Info(context.Background())
		if infoErr != nil {
			t.Fatal(infoErr)
		}
		if info.Name == "submit_recovery_proposal" {
			submit = candidate.(tool.InvokableTool)
		}
	}
	limits := map[string]domain.AgentBudget{RecoveryAgentName: {MaxIterations: 2, MaxToolUses: 2, MaxToolCost: 2, MaxTokens: 1000, MaxCorrections: 1}}
	budget := safety.NewBudgetController(nil, limits, domain.AgentBudget{MaxToolUses: 2, MaxToolCost: 2, MaxTokens: 1000}, nil)
	incident := &domain.Incident{ID: "recovery-policy", Status: domain.StatusProposing, Namespace: "kubepilot-demo", Resource: "gateway-service", RootCauseResource: "gateway-service"}
	runtime := &constrainedRuntime{state: &WorkflowState{Incident: incident}, budgets: budget, done: map[string]bool{}}
	input := RecoveryDecision{Action: domain.ActionRestartPod, Target: "other/gateway-service", Reason: "restart", Risk: "brief disruption", Diff: "rollout annotation", Rollback: "restore prior annotation", Parameters: map[string]any{}, Confidence: .9}
	payload, _ := json.Marshal(input)
	raw, err := submit.InvokableRun(withConstrainedRuntime(context.Background(), runtime), string(payload))
	if err != nil {
		t.Fatal(err)
	}
	var output constrainedToolOutput
	if err = json.Unmarshal([]byte(raw), &output); err != nil {
		t.Fatal(err)
	}
	if output.Feedback == nil || output.Feedback.Category != domain.SafetyFatal || incident.Status != domain.StatusNeedsAttention {
		t.Fatalf("namespace override was not fatal: feedback=%+v status=%s", output.Feedback, incident.Status)
	}
}
