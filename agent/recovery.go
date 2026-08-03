package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/kubepilot-aiops/kubepilot/internal/domain"
)

func canonicalProposalTarget(target, namespace, resource string) (string, error) {
	if target == "" || target == resource {
		return resource, nil
	}
	parts := strings.Split(target, "/")
	if len(parts) == 2 && parts[0] == namespace && parts[1] == resource {
		return resource, nil
	}
	return "", fmt.Errorf("proposal target %q does not match incident resource %q in namespace %q", target, resource, namespace)
}

type KubernetesExecutor struct {
	Client interface {
		RestartDeployment(context.Context, string, string) error
		ScaleDeployment(context.Context, string, string, int32) error
		RollbackDeployment(context.Context, string, string) error
		TargetState(context.Context, string, string) (string, string, bool, int32, error)
	}
	Guard interface {
		ClaimAction(context.Context, string, time.Duration) (bool, error)
		CompleteAction(context.Context, string, string, time.Duration) error
	}
}

var ErrActionResultUnknown = errors.New("Kubernetes action result is unknown and must not be replayed")

type kubernetesDryRunner interface {
	DryRunRestartDeployment(context.Context, string, string, string) (map[string]any, map[string]any, error)
	DryRunScaleDeployment(context.Context, string, string, int32) (map[string]any, map[string]any, error)
	DryRunRollbackDeployment(context.Context, string, string) (map[string]any, map[string]any, error)
}

type restartAtExecutor interface {
	RestartDeploymentAt(context.Context, string, string, string) error
}

func (e KubernetesExecutor) Prepare(ctx context.Context, p *domain.RecoveryProposal) error {
	uid, rv, _, _, err := e.Client.TargetState(ctx, p.Namespace, p.Target)
	if err != nil {
		return err
	}
	p.TargetUID, p.ResourceVersion = uid, rv
	return nil
}

func (e KubernetesExecutor) DryRun(ctx context.Context, p *domain.RecoveryProposal) (*domain.DryRunResult, error) {
	if err := e.Prepare(ctx, p); err != nil {
		return &domain.DryRunResult{Action: p.Action, Target: p.Target, Error: err.Error(), ValidatedAt: time.Now().UTC()}, err
	}
	runner, ok := e.Client.(kubernetesDryRunner)
	if !ok {
		return &domain.DryRunResult{Action: p.Action, Target: p.Target, Error: "Kubernetes DryRunAll is unavailable", ValidatedAt: time.Now().UTC()}, fmt.Errorf("Kubernetes DryRunAll is unavailable")
	}
	var before, after map[string]any
	var err error
	switch p.Action {
	case domain.ActionRestartPod:
		restartedAt := time.Now().UTC().Format(time.RFC3339Nano)
		if p.Parameters == nil {
			p.Parameters = map[string]any{}
		}
		p.Parameters["restarted_at"] = restartedAt
		before, after, err = runner.DryRunRestartDeployment(ctx, p.Namespace, p.Target, restartedAt)
	case domain.ActionScaleDeployment:
		var replicas int32
		replicas, err = proposalReplicas(p.Parameters)
		if err == nil {
			before, after, err = runner.DryRunScaleDeployment(ctx, p.Namespace, p.Target, replicas)
		}
	case domain.ActionRollbackDeployment:
		before, after, err = runner.DryRunRollbackDeployment(ctx, p.Namespace, p.Target)
	default:
		err = fmt.Errorf("unsupported recovery action %q", p.Action)
	}
	result := &domain.DryRunResult{Action: p.Action, Target: p.Target, Before: before, After: after, ValidatedAt: time.Now().UTC()}
	if err != nil {
		result.Error = err.Error()
		return result, err
	}
	payload, _ := json.Marshal(struct {
		Action     domain.RecoveryAction `json:"action"`
		Namespace  string                `json:"namespace"`
		Target     string                `json:"target"`
		UID        string                `json:"uid"`
		Version    string                `json:"resource_version"`
		Parameters map[string]any        `json:"parameters"`
		After      map[string]any        `json:"after"`
	}{p.Action, p.Namespace, p.Target, p.TargetUID, p.ResourceVersion, p.Parameters, after})
	hash := sha256.Sum256(payload)
	result.Success = true
	result.MutationSpecHash = hex.EncodeToString(hash[:])
	return result, nil
}

func (e KubernetesExecutor) Execute(ctx context.Context, in *domain.Incident, p domain.RecoveryProposal) error {
	uid, rv, _, _, err := e.Client.TargetState(ctx, p.Namespace, p.Target)
	if err != nil {
		return err
	}
	if p.TargetUID == "" || p.ResourceVersion == "" {
		return fmt.Errorf("proposal target preconditions are missing")
	}
	if uid != p.TargetUID || rv != p.ResourceVersion {
		return fmt.Errorf("target changed since proposal: uid/resourceVersion mismatch")
	}
	if in == nil || in.ExecutionContext == nil || in.ExecutionContext.IdempotencyKey == "" {
		return fmt.Errorf("approved execution context is required")
	}
	if e.Guard == nil {
		return fmt.Errorf("persistent action guard is required")
	}
	actionKey := in.ID + ":" + in.ExecutionContext.IdempotencyKey
	claimed, err := e.Guard.ClaimAction(ctx, actionKey, 24*time.Hour)
	if err != nil {
		return fmt.Errorf("claim action: %w", err)
	}
	if !claimed {
		return ErrActionResultUnknown
	}
	completion := "failed"
	defer func() {
		_ = e.Guard.CompleteAction(context.WithoutCancel(ctx), actionKey, completion, 24*time.Hour)
	}()

	var executeErr error
	switch p.Action {
	case domain.ActionRestartPod:
		if restartedAt, ok := p.Parameters["restarted_at"].(string); ok {
			if client, supported := e.Client.(restartAtExecutor); supported {
				executeErr = client.RestartDeploymentAt(ctx, p.Namespace, p.Target, restartedAt)
				break
			}
		}
		executeErr = e.Client.RestartDeployment(ctx, p.Namespace, p.Target)
	case domain.ActionScaleDeployment:
		replicas, parseErr := proposalReplicas(p.Parameters)
		if parseErr != nil {
			executeErr = parseErr
			break
		}
		executeErr = e.Client.ScaleDeployment(ctx, p.Namespace, p.Target, replicas)
	case domain.ActionRollbackDeployment:
		executeErr = e.Client.RollbackDeployment(ctx, p.Namespace, p.Target)
	default:
		executeErr = fmt.Errorf("unsupported action")
	}
	if executeErr == nil {
		completion = "succeeded"
	}
	return executeErr
}

func proposalReplicas(parameters map[string]any) (int32, error) {
	value, ok := parameters["replicas"]
	if !ok {
		return 0, fmt.Errorf("replicas is required")
	}
	var replicas int64
	switch number := value.(type) {
	case float64:
		replicas = int64(number)
	case int:
		replicas = int64(number)
	case int32:
		replicas = int64(number)
	case int64:
		replicas = number
	case json.Number:
		parsed, err := number.Int64()
		if err != nil {
			return 0, fmt.Errorf("replicas must be an integer")
		}
		replicas = parsed
	default:
		return 0, fmt.Errorf("replicas must be an integer")
	}
	if replicas < 1 || replicas > 10 {
		return 0, fmt.Errorf("replicas must be between 1 and 10")
	}
	return int32(replicas), nil
}
func (e KubernetesExecutor) Verify(ctx context.Context, in *domain.Incident) (domain.Verification, error) {
	deadline := time.NewTimer(2 * time.Minute)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	consecutive := 0
	var previousRestarts int32 = -1
	target := in.Resource
	if in.Proposal != nil && in.Proposal.Target != "" {
		target = in.Proposal.Target
	}
	for {
		_, _, ready, restarts, err := e.Client.TargetState(ctx, in.Namespace, target)
		stable := err == nil && ready && (previousRestarts < 0 || restarts <= previousRestarts)
		if stable {
			consecutive++
		} else {
			consecutive = 0
		}
		previousRestarts = restarts
		if consecutive >= 3 {
			return domain.Verification{Success: true, Checks: map[string]bool{"pod_ready": true, "deployment_available": true, "restarts_stable": true}, Message: "Kubernetes health checks passed three consecutive samples", CompletedAt: time.Now().UTC()}, nil
		}
		select {
		case <-ctx.Done():
			return domain.Verification{}, ctx.Err()
		case <-deadline.C:
			return domain.Verification{Success: false, Checks: map[string]bool{"pod_ready": ready, "restarts_stable": stable}, Message: "verification timed out", CompletedAt: time.Now().UTC()}, nil
		case <-ticker.C:
		}
	}
}
