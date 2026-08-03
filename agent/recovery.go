package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/kubepilot-aiops/kubepilot/internal/domain"
	llm "github.com/kubepilot-aiops/kubepilot/internal/model"
	"github.com/oklog/ulid/v2"
)

type RecoveryAgent struct{ Model llm.Client }

func (a RecoveryAgent) Propose(ctx context.Context, in *domain.Incident) error {
	incidentView := *in
	incidentView.Evidence = nil
	incidentView.Alerts = nil
	incidentView.Verification = nil
	payload, _ := json.Marshal(incidentView)
	tool := recoveryTool()
	prompt := `Call submit_recovery exactly once with a safe Kubernetes recovery proposal. Allowed action values are restart_pod, scale_deployment, rollback_deployment. Do not invent shell commands. Target must be the incident workload name without a namespace or resource kind.`
	resp, err := a.Model.Complete(ctx, []llm.Message{{Role: "system", Content: prompt}, {Role: "user", Content: string(payload)}}, []llm.Tool{tool})
	if err != nil {
		return err
	}
	output, err := responseJSON(resp, tool.Name)
	if err != nil {
		return err
	}
	var p domain.RecoveryProposal
	if err = json.Unmarshal([]byte(stripFence(output)), &p); err != nil {
		repaired, repairErr := a.Model.Complete(ctx, []llm.Message{{Role: "system", Content: "Repair this invalid proposal without adding shell commands and call submit_recovery exactly once."}, {Role: "user", Content: output}}, []llm.Tool{tool})
		if repairErr != nil {
			return fmt.Errorf("invalid recovery JSON: %w", err)
		}
		repairedOutput, outputErr := responseJSON(repaired, tool.Name)
		if outputErr != nil {
			return fmt.Errorf("invalid recovery JSON: %w", outputErr)
		}
		if err = json.Unmarshal([]byte(stripFence(repairedOutput)), &p); err != nil {
			return fmt.Errorf("invalid recovery JSON after one repair: %w", err)
		}
	}
	switch p.Action {
	case domain.ActionRestartPod, domain.ActionScaleDeployment, domain.ActionRollbackDeployment:
	default:
		return fmt.Errorf("unsupported recovery action %q", p.Action)
	}
	p.Target, err = canonicalProposalTarget(p.Target, in.Namespace, in.Resource)
	if err != nil {
		return err
	}
	p.ID = ulid.Make().String()
	p.Namespace = in.Namespace
	p.ExpiresAt = time.Now().UTC().Add(15 * time.Minute)
	in.Proposal = &p
	return nil
}

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

func recoveryTool() llm.Tool {
	return llm.Tool{Name: "submit_recovery", Description: "Submit one constrained Kubernetes recovery proposal.", InputSchema: map[string]any{
		"type": "object",
		"properties": map[string]any{
			"action":     map[string]any{"type": "string", "enum": []string{"restart_pod", "scale_deployment", "rollback_deployment"}},
			"target":     map[string]any{"type": "string", "description": "workload name only, without namespace or resource kind"},
			"parameters": map[string]any{"type": "object"},
			"reason":     map[string]any{"type": "string"},
			"risk":       map[string]any{"type": "string"},
			"diff":       map[string]any{"type": "string"},
			"rollback":   map[string]any{"type": "string"},
			"confidence": map[string]any{"type": "number", "minimum": 0, "maximum": 1},
		},
		"required":             []string{"action", "target", "parameters", "reason", "risk", "diff", "rollback", "confidence"},
		"additionalProperties": false,
	}}
}

type KubernetesExecutor struct {
	Client interface {
		RestartDeployment(context.Context, string, string) error
		ScaleDeployment(context.Context, string, string, int32) error
		RollbackDeployment(context.Context, string, string) error
		TargetState(context.Context, string, string) (string, string, bool, int32, error)
	}
}

func (e KubernetesExecutor) Prepare(ctx context.Context, p *domain.RecoveryProposal) error {
	uid, rv, _, _, err := e.Client.TargetState(ctx, p.Namespace, p.Target)
	if err != nil {
		return err
	}
	p.TargetUID, p.ResourceVersion = uid, rv
	return nil
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
	switch p.Action {
	case domain.ActionRestartPod:
		return e.Client.RestartDeployment(ctx, p.Namespace, p.Target)
	case domain.ActionScaleDeployment:
		v, ok := p.Parameters["replicas"].(float64)
		if !ok {
			return fmt.Errorf("replicas is required")
		}
		return e.Client.ScaleDeployment(ctx, p.Namespace, p.Target, int32(v))
	case domain.ActionRollbackDeployment:
		return e.Client.RollbackDeployment(ctx, p.Namespace, p.Target)
	default:
		return fmt.Errorf("unsupported action")
	}
}
func (e KubernetesExecutor) Verify(ctx context.Context, in *domain.Incident) (domain.Verification, error) {
	deadline := time.NewTimer(2 * time.Minute)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	consecutive := 0
	var previousRestarts int32 = -1
	for {
		_, _, ready, restarts, err := e.Client.TargetState(ctx, in.Namespace, in.Resource)
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
