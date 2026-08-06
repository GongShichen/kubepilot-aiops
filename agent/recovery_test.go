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
	captools "github.com/kubepilot-aiops/kubepilot/tools"
)

func TestRecoveryRejectsAnotherNamespace(t *testing.T) {
	if _, err := canonicalProposalTarget("other/gateway-service", "kubepilot-benchmark", "gateway-service"); err == nil {
		t.Fatal("expected cross-namespace target to be rejected")
	}
}

type recoveryTestClient struct {
	restarts   int
	restartAts int
	scales     int
	rollbacks  int
	uid        string
	rv         string
	ready      bool
	restartsAt int32
	stateErr   error
	restartErr error
	dryRunErr  error
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

func (c *recoveryTestClient) RestartDeploymentAt(context.Context, string, string, string) error {
	c.restartAts++
	return c.restartErr
}
func (c *recoveryTestClient) ScaleDeployment(context.Context, string, string, int32) error {
	c.scales++
	return nil
}
func (c *recoveryTestClient) RollbackDeployment(context.Context, string, string) error {
	c.rollbacks++
	return nil
}
func (c *recoveryTestClient) TargetState(context.Context, string, string) (string, string, bool, int32, error) {
	if c.stateErr != nil {
		return "", "", false, 0, c.stateErr
	}
	uid, rv := c.uid, c.rv
	if uid == "" {
		uid = "uid-1"
	}
	if rv == "" {
		rv = "rv-1"
	}
	ready := c.ready
	if !ready && c.restartsAt == 0 {
		ready = true
	}
	return uid, rv, ready, c.restartsAt, nil
}
func (c *recoveryTestClient) DryRunRestartDeployment(context.Context, string, string, string) (map[string]any, map[string]any, error) {
	return map[string]any{"annotation": "before"}, map[string]any{"annotation": "after"}, c.dryRunErr
}
func (c *recoveryTestClient) DryRunScaleDeployment(_ context.Context, _, _ string, replicas int32) (map[string]any, map[string]any, error) {
	return map[string]any{"replicas": int32(1)}, map[string]any{"replicas": replicas}, c.dryRunErr
}
func (c *recoveryTestClient) DryRunRollbackDeployment(context.Context, string, string) (map[string]any, map[string]any, error) {
	return map[string]any{"revision": 2}, map[string]any{"revision": 1}, c.dryRunErr
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

func TestRecoveryDryRunCoversSupportedActionsAndFailures(t *testing.T) {
	client := &recoveryTestClient{}
	executor := KubernetesExecutor{Client: client}
	for _, test := range []struct {
		name       string
		action     domain.RecoveryAction
		parameters map[string]any
	}{
		{name: "restart", action: domain.ActionRestartPod},
		{name: "scale", action: domain.ActionScaleDeployment, parameters: map[string]any{"replicas": json.Number("3")}},
		{name: "rollback", action: domain.ActionRollbackDeployment},
	} {
		t.Run(test.name, func(t *testing.T) {
			proposal := &domain.RecoveryProposal{Action: test.action, Namespace: "kubepilot-demo", Target: "gateway-service", Parameters: test.parameters}
			result, err := executor.DryRun(context.Background(), proposal)
			if err != nil || !result.Success || result.MutationSpecHash == "" || proposal.TargetUID == "" || proposal.ResourceVersion == "" {
				t.Fatalf("dry-run failed: result=%+v err=%v proposal=%+v", result, err, proposal)
			}
		})
	}

	invalid := &domain.RecoveryProposal{Action: domain.ActionScaleDeployment, Namespace: "kubepilot-demo", Target: "gateway-service", Parameters: map[string]any{"replicas": 11}}
	if result, err := executor.DryRun(context.Background(), invalid); err == nil || result.Success || result.Error == "" {
		t.Fatalf("invalid replicas were accepted: result=%+v err=%v", result, err)
	}
	unsupported := &domain.RecoveryProposal{Action: domain.RecoveryAction("delete"), Namespace: "kubepilot-demo", Target: "gateway-service"}
	if result, err := executor.DryRun(context.Background(), unsupported); err == nil || result.Error == "" {
		t.Fatalf("unsupported action was accepted: result=%+v err=%v", result, err)
	}
	client.stateErr = errors.New("target unavailable")
	if result, err := executor.DryRun(context.Background(), &domain.RecoveryProposal{Action: domain.ActionRestartPod, Namespace: "kubepilot-demo", Target: "gateway-service"}); err == nil || result.Error == "" {
		t.Fatalf("prepare failure was not surfaced: result=%+v err=%v", result, err)
	}
}

type recoveryMutationOnly struct{ base *recoveryTestClient }

func (c recoveryMutationOnly) RestartDeployment(ctx context.Context, namespace, target string) error {
	return c.base.RestartDeployment(ctx, namespace, target)
}
func (c recoveryMutationOnly) ScaleDeployment(ctx context.Context, namespace, target string, replicas int32) error {
	return c.base.ScaleDeployment(ctx, namespace, target, replicas)
}
func (c recoveryMutationOnly) RollbackDeployment(ctx context.Context, namespace, target string) error {
	return c.base.RollbackDeployment(ctx, namespace, target)
}
func (c recoveryMutationOnly) TargetState(ctx context.Context, namespace, target string) (string, string, bool, int32, error) {
	return c.base.TargetState(ctx, namespace, target)
}

func TestRecoveryRejectsMissingDryRunCapabilityAndStaleSnapshot(t *testing.T) {
	client := &recoveryTestClient{}
	executor := KubernetesExecutor{Client: recoveryMutationOnly{base: client}}
	proposal := &domain.RecoveryProposal{Action: domain.ActionRestartPod, Namespace: "kubepilot-demo", Target: "gateway-service"}
	if result, err := executor.DryRun(context.Background(), proposal); err == nil || result.Error == "" {
		t.Fatalf("missing dry-run capability was accepted: result=%+v err=%v", result, err)
	}

	full := KubernetesExecutor{Client: &recoveryTestClient{}}
	if err := full.ValidateDryRunFreshness(context.Background(), proposal, &domain.DryRunResult{Success: true, ValidatedAt: time.Now().Add(-3 * time.Minute)}); err == nil {
		t.Fatal("expired dry-run snapshot was accepted")
	}
}

func TestRecoveryExecuteAndVerifySupportedActions(t *testing.T) {
	client := &recoveryTestClient{ready: true, restartsAt: 4}
	incident := &domain.Incident{ID: "recovery-actions", Namespace: "kubepilot-demo", Resource: "gateway-service", ExecutionContext: &domain.ExecutionContext{IdempotencyKey: "approved"}}
	for _, proposal := range []domain.RecoveryProposal{
		{Action: domain.ActionRestartPod, Namespace: incident.Namespace, Target: incident.Resource, TargetUID: "uid-1", ResourceVersion: "rv-1", Parameters: map[string]any{"restarted_at": "fixed"}},
		{Action: domain.ActionScaleDeployment, Namespace: incident.Namespace, Target: incident.Resource, TargetUID: "uid-1", ResourceVersion: "rv-1", Parameters: map[string]any{"replicas": int32(2)}},
		{Action: domain.ActionRollbackDeployment, Namespace: incident.Namespace, Target: incident.Resource, TargetUID: "uid-1", ResourceVersion: "rv-1"},
	} {
		executor := KubernetesExecutor{Client: client, Guard: &recoveryTestGuard{}}
		if err := executor.Execute(context.Background(), incident, proposal); err != nil {
			t.Fatalf("execute %s: %v", proposal.Action, err)
		}
	}
	if client.restartAts != 1 || client.scales != 1 || client.rollbacks != 1 {
		t.Fatalf("unexpected mutation counts: restartAt=%d scale=%d rollback=%d", client.restartAts, client.scales, client.rollbacks)
	}
	incident.Proposal = &domain.RecoveryProposal{Target: "gateway-service"}
	verification, err := (KubernetesExecutor{Client: client}).Verify(context.Background(), incident)
	if err != nil || !verification.Success || verification.RestartCount == nil || *verification.RestartCount != 4 || !verification.Checks["deployment_available"] {
		t.Fatalf("verification mismatch: %+v err=%v", verification, err)
	}
}

func TestProposalReplicasAcceptedRepresentations(t *testing.T) {
	for _, value := range []any{float64(2), 2, int32(2), int64(2), json.Number("2")} {
		if replicas, err := proposalReplicas(map[string]any{"replicas": value}); err != nil || replicas != 2 {
			t.Fatalf("value %T(%v): replicas=%d err=%v", value, value, replicas, err)
		}
	}
	for _, parameters := range []map[string]any{{}, {"replicas": "two"}, {"replicas": json.Number("2.5")}, {"replicas": 0}} {
		if _, err := proposalReplicas(parameters); err == nil {
			t.Fatalf("invalid replicas accepted: %+v", parameters)
		}
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
	capabilities, err := buildConstrainedRecoveryCapabilities(constrainedToolDeps{})
	if err != nil {
		t.Fatal(err)
	}
	tools := registeredCapabilitiesForTest(t, captools.NodeRecoveryReact, capabilities)
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
	limits := map[string]domain.AgentBudget{RecoveryAgentName: {MaxIterations: 2, MaxToolUses: 2, MaxTokens: 1000, MaxCorrections: 1}}
	budget := safety.NewBudgetController(nil, limits, nil)
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

func TestRecoveryCapabilitiesRejectUnsafeAndIncompleteProposals(t *testing.T) {
	capabilities, err := buildConstrainedRecoveryCapabilities(constrainedToolDeps{Executor: &graphExecutor{}})
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]tool.InvokableTool{}
	for _, candidate := range registeredCapabilitiesForTest(t, captools.NodeRecoveryReact, capabilities) {
		info, infoErr := candidate.Info(context.Background())
		if infoErr != nil {
			t.Fatal(infoErr)
		}
		byName[info.Name] = candidate.(tool.InvokableTool)
	}
	invoke := func(name, payload string, configure func(*WorkflowState)) constrainedToolOutput {
		t.Helper()
		incident := &domain.Incident{ID: "recovery-contract", Status: domain.StatusProposing, Namespace: "kubepilot-demo", Service: "gateway-service", Resource: "gateway-service", RootCauseResource: "gateway-service", AgentBudget: &domain.AgentBudgetState{}}
		state := &WorkflowState{Workflow: WorkflowName, Incident: incident}
		if configure != nil {
			configure(state)
		}
		budget := safety.NewBudgetController(incident.AgentBudget, map[string]domain.AgentBudget{RecoveryAgentName: {MaxIterations: 5, MaxToolUses: 10, MaxTokens: 10000, MaxCorrections: 4}}, map[string]int{})
		runtime := &constrainedRuntime{state: state, budgets: budget, done: map[string]bool{}}
		raw, callErr := byName[name].InvokableRun(withConstrainedRuntime(context.Background(), runtime), payload)
		if callErr != nil {
			t.Fatalf("%s: %v", name, callErr)
		}
		var output constrainedToolOutput
		if decodeErr := json.Unmarshal([]byte(raw), &output); decodeErr != nil {
			t.Fatalf("%s output: %v", name, decodeErr)
		}
		return output
	}

	if output := invoke("submit_recovery_proposal", `{"action":"delete_workload","target":"gateway-service","parameters":{},"reason":"remove","risk":"data loss","diff":"delete","rollback":"none","confidence":0.9}`, nil); output.Feedback == nil || output.Feedback.Category != domain.SafetyFatal {
		t.Fatalf("forbidden action feedback=%+v", output)
	}
	if output := invoke("submit_recovery_proposal", `{"action":"restart_pod","target":"gateway-service","parameters":{},"reason":"kubectl delete pod","risk":"brief disruption","diff":"restart","rollback":"wait","confidence":0.9}`, nil); output.Feedback == nil || output.Feedback.Code != "free_form_execution_forbidden" {
		t.Fatalf("free-form execution feedback=%+v", output)
	}
	if output := invoke("submit_recovery_proposal", `{"action":"restart_pod","target":"other-service","parameters":{},"reason":"restart","risk":"brief disruption","diff":"restart","rollback":"wait","confidence":0.9}`, nil); output.Feedback == nil || output.Feedback.Code != "proposal_target_invalid" || !output.Feedback.Retryable {
		t.Fatalf("invalid target feedback=%+v", output)
	}
	if output := invoke("dry_run_recovery_proposal", `{}`, nil); output.Feedback == nil || output.Feedback.Code != "proposal_incomplete" {
		t.Fatalf("missing proposal feedback=%+v", output)
	}
	if output := invoke("accept_recovery_proposal", `{}`, nil); output.Feedback == nil || output.Feedback.Code != "dry_run_required" {
		t.Fatalf("missing dry-run feedback=%+v", output)
	}
	if output := invoke("accept_recovery_proposal", `{}`, func(state *WorkflowState) {
		state.Incident.Proposal = &domain.RecoveryProposal{Action: domain.ActionRestartPod, Namespace: state.Incident.Namespace, Target: state.Incident.Resource}
		state.DryRun = &domain.DryRunResult{Success: true, Action: domain.ActionRestartPod, Target: state.Incident.Resource, ValidatedAt: time.Now().Add(-3 * time.Minute)}
	}); output.Feedback == nil || output.Feedback.Code != "dry_run_expired" {
		t.Fatalf("expired dry-run feedback=%+v", output)
	}
	if output := invoke("escalate_recovery", `{}`, nil); output.Feedback == nil || !output.Feedback.RequiresHuman || output.Feedback.Code != "agent_requested_human" {
		t.Fatalf("recovery escalation feedback=%+v", output)
	}
}
