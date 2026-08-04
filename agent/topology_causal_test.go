package agent

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/cloudwego/eino/components/tool"
	"github.com/kubepilot-aiops/kubepilot/internal/causal"
	causalknowledge "github.com/kubepilot-aiops/kubepilot/internal/causal/knowledge"
	"github.com/kubepilot-aiops/kubepilot/internal/domain"
	"github.com/kubepilot-aiops/kubepilot/internal/safety"
	topologyknowledge "github.com/kubepilot-aiops/kubepilot/internal/topology/knowledge"
	"github.com/kubepilot-aiops/kubepilot/reasoning"
)

func TestIncidentGraphAndCausalToolsShareWorkflowState(t *testing.T) {
	tools, err := buildConstrainedDiagnosisTools(constrainedToolDeps{Reasoning: reasoning.New(reasoning.DefaultConfig()), Causal: causal.DefaultMatcher()})
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]tool.InvokableTool{}
	for _, candidate := range tools {
		info, infoErr := candidate.Info(context.Background())
		if infoErr != nil {
			t.Fatal(infoErr)
		}
		if invokable, ok := candidate.(tool.InvokableTool); ok {
			byName[info.Name] = invokable
		}
	}
	incident := &domain.Incident{ID: "graph-causal", Status: domain.StatusDiagnosing, Namespace: "kubepilot-demo", Service: "payment-service", Resource: "payment-service", EvidenceStartAt: time.Now().Add(-time.Minute), Evidence: []domain.Evidence{{ID: "metric", Source: "prometheus", Type: "memory_metric", Summary: "memory growth", Service: "payment-service", Content: map[string]any{"dependency": "mysql"}}, {ID: "event", Source: "kubernetes", Type: "event", Summary: "OOMKilled pod restart", Service: "payment-service"}, {ID: "log", Source: "loki", Summary: "error rate increase", Service: "payment-service"}}}
	budget := safety.NewBudgetController(&domain.AgentBudgetState{}, map[string]domain.AgentBudget{DiagnosisAgentName: {MaxToolUses: 20, MaxToolCost: 40, MaxTokens: 10000, MaxCorrections: 2}}, domain.AgentBudget{MaxToolUses: 20, MaxToolCost: 40, MaxTokens: 10000}, map[string]int{})
	state := &WorkflowState{Incident: incident}
	runtime := &constrainedRuntime{state: state, budgets: budget, done: map[string]bool{}}
	ctx := withConstrainedRuntime(context.Background(), runtime)
	for _, name := range []string{"build_incident_graph", "match_causal_patterns"} {
		if byName[name] == nil {
			t.Fatalf("missing %s", name)
		}
		payload, _ := json.Marshal(map[string]any{})
		if _, runErr := byName[name].InvokableRun(ctx, string(payload)); runErr != nil {
			t.Fatalf("%s failed: %v", name, runErr)
		}
	}
	if state.IncidentGraph == nil || len(state.IncidentGraph.Edges) == 0 {
		t.Fatalf("graph was not persisted: %+v", state.IncidentGraph)
	}
	if len(state.CausalMatches) == 0 || state.CausalMatches[0].Cause != "memory_leak" {
		t.Fatalf("causal matches were not persisted: %+v", state.CausalMatches)
	}
}

func TestKnowledgeToolsAreReadProposalAndValidationOnly(t *testing.T) {
	topologyStore := topologyknowledge.NewMemoryStore()
	causalStore := causalknowledge.NewMemoryStore()
	tools, err := buildConstrainedDiagnosisTools(constrainedToolDeps{Reasoning: reasoning.New(reasoning.DefaultConfig()), TopologyPatterns: topologyStore, CausalPatterns: causalStore})
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]tool.InvokableTool{}
	for _, candidate := range tools {
		info, infoErr := candidate.Info(context.Background())
		if infoErr != nil {
			t.Fatal(infoErr)
		}
		if invokable, ok := candidate.(tool.InvokableTool); ok {
			byName[info.Name] = invokable
		}
	}
	incident := &domain.Incident{ID: "knowledge-tools", Status: domain.StatusDiagnosing, Namespace: "kubepilot-demo", Service: "payment-service", Resource: "payment-service", Confidence: .9, Evidence: []domain.Evidence{{ID: "m", Source: "prometheus", Type: "memory_metric", Summary: "memory growth"}, {ID: "k", Source: "kubernetes", Type: "kubernetes_event", Summary: "OOMKilled pod restart"}}}
	state := &WorkflowState{Incident: incident}
	budget := safety.NewBudgetController(&domain.AgentBudgetState{}, map[string]domain.AgentBudget{DiagnosisAgentName: {MaxToolUses: 20, MaxToolCost: 40, MaxTokens: 10000, MaxCorrections: 2}}, domain.AgentBudget{MaxToolUses: 20, MaxToolCost: 40, MaxTokens: 10000}, map[string]int{})
	runtime := &constrainedRuntime{state: state, budgets: budget, done: map[string]bool{}}
	ctx := withConstrainedRuntime(context.Background(), runtime)
	if _, err = byName["propose_causal_pattern"].InvokableRun(ctx, `{"cause":"memory leak","causal_path":["memory_leak","memory_growth","oom_killed"],"evidence_ids":["m","k"]}`); err != nil {
		t.Fatal(err)
	}
	if state.CausalProposal == nil {
		t.Fatal("proposal was not retained in workflow state")
	}
	if _, err = byName["validate_causal_pattern"].InvokableRun(ctx, `{}`); err != nil {
		t.Fatal(err)
	}
	if state.CausalValidation == nil || !state.CausalValidation.Valid {
		t.Fatalf("proposal was not validated: %+v", state.CausalValidation)
	}
	if items, _ := causalStore.List(context.Background(), "", 10); len(items) != 0 {
		t.Fatal("Agent tool wrote causal knowledge directly")
	}
}
