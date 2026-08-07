package agent

import (
	"context"
	"fmt"

	"github.com/cloudwego/eino/compose"
	"github.com/kubepilot-aiops/kubepilot/internal/domain"
)

// buildBaselineGraph preserves the frozen diagnosis behavior of direct, RAG,
// ReAct, rule-only, evidence-only, cognitive, and active-diagnosis. It is a
// separate Eino subgraph so none of these nodes are reachable from the
// KubePilot Brain loop.
func buildBaselineGraph(deps SupervisorDeps, runtimeDeps constrainedToolDeps, transition func(context.Context, *domain.Incident, domain.IncidentStatus) error) (*compose.Graph[*WorkflowState, *WorkflowState], error) {
	graph := compose.NewGraph[*WorkflowState, *WorkflowState]()
	add := func(name string, fn func(context.Context, *WorkflowState) (*WorkflowState, error)) error {
		return graph.AddLambdaNode(name, compose.InvokableLambda(fn), compose.WithNodeName(name))
	}
	if err := add("baseline_router", func(_ context.Context, state *WorkflowState) (*WorkflowState, error) {
		return state, nil
	}); err != nil {
		return nil, err
	}
	if err := add("baseline_strategy", func(ctx context.Context, state *WorkflowState) (*WorkflowState, error) {
		if err := deps.Agents.RunBaseline(ctx, state, runtimeDeps); err != nil {
			return state, err
		}
		state.Incident.DiagnosisLedger = &state.DiagnosisLedger
		return state, nil
	}); err != nil {
		return nil, err
	}
	if err := add("cognitive_intent", func(ctx context.Context, state *WorkflowState) (*WorkflowState, error) {
		if err := deps.Agents.cognitiveIntentNode(ctx, state); err != nil {
			return state, err
		}
		return state, nil
	}); err != nil {
		return nil, err
	}
	if err := add("query_compiler", func(_ context.Context, state *WorkflowState) (*WorkflowState, error) {
		if err := deps.Agents.queryCompilerNode(state); err != nil {
			return state, err
		}
		return state, nil
	}); err != nil {
		return nil, err
	}
	if err := add("evidence_collection", func(ctx context.Context, state *WorkflowState) (*WorkflowState, error) {
		if err := deps.Agents.evidenceCollectionNode(ctx, state, runtimeDeps); err != nil {
			return state, err
		}
		return state, nil
	}); err != nil {
		return nil, err
	}
	if err := add("signal_assertion_builder", func(_ context.Context, state *WorkflowState) (*WorkflowState, error) {
		if err := deps.Agents.signalAssertionBuilderNode(state, runtimeDeps); err != nil {
			return state, err
		}
		return state, nil
	}); err != nil {
		return nil, err
	}
	if err := add("candidate_generation", func(ctx context.Context, state *WorkflowState) (*WorkflowState, error) {
		if err := deps.Agents.candidateGenerationNode(ctx, state, runtimeDeps); err != nil {
			return state, err
		}
		return state, nil
	}); err != nil {
		return nil, err
	}
	if err := add("cognitive_reasoning", func(ctx context.Context, state *WorkflowState) (*WorkflowState, error) {
		if err := deps.Agents.cognitiveReasoningNode(ctx, state); err != nil {
			return state, err
		}
		return state, nil
	}); err != nil {
		return nil, err
	}
	if err := add("causal_falsification", func(_ context.Context, state *WorkflowState) (*WorkflowState, error) {
		if err := deps.Agents.causalFalsificationNode(state); err != nil {
			return state, err
		}
		return state, nil
	}); err != nil {
		return nil, err
	}
	if err := add("objective_arbitration", func(_ context.Context, state *WorkflowState) (*WorkflowState, error) {
		if err := deps.Agents.objectiveArbitrationNode(state); err != nil {
			return state, err
		}
		return state, nil
	}); err != nil {
		return nil, err
	}
	if err := add("recovery_permission", func(ctx context.Context, state *WorkflowState) (*WorkflowState, error) {
		if state.DiagnosisRuntime == nil {
			return state, fmt.Errorf("baseline recovery permission is missing diagnosis runtime state")
		}
		if !state.DiagnosisRuntime.Completed {
			return state, fmt.Errorf("baseline recovery permission reached before diagnosis completed")
		}
		result, err := cognitiveDiagnosisResult(state)
		if err != nil {
			return state, err
		}
		if err = deps.Agents.applyDiagnosisResult(ctx, state, runtimeDeps, result); err != nil {
			return state, err
		}
		return state, nil
	}); err != nil {
		return nil, err
	}
	if err := add("constrained_recovery_agents", func(ctx context.Context, state *WorkflowState) (*WorkflowState, error) {
		if state.Incident.Status == domain.StatusNeedsAttention {
			return state, nil
		}
		if err := deps.Agents.runConstrainedAgents(ctx, state, runtimeDeps); err != nil {
			return state, err
		}
		state.Incident.DiagnosisLedger = &state.DiagnosisLedger
		return state, nil
	}); err != nil {
		return nil, err
	}

	for _, edge := range [][2]string{
		{compose.START, "baseline_router"},
		{"cognitive_intent", "query_compiler"},
		{"query_compiler", "evidence_collection"},
		{"evidence_collection", "signal_assertion_builder"},
		{"signal_assertion_builder", "candidate_generation"},
		{"candidate_generation", "cognitive_reasoning"},
		{"cognitive_reasoning", "causal_falsification"},
		{"causal_falsification", "objective_arbitration"},
		{"baseline_strategy", compose.END},
		{"constrained_recovery_agents", compose.END},
	} {
		if err := graph.AddEdge(edge[0], edge[1]); err != nil {
			return nil, err
		}
	}
	if err := graph.AddBranch("baseline_router", compose.NewGraphBranch(func(_ context.Context, state *WorkflowState) (string, error) {
		if state == nil || state.Incident == nil {
			return "", fmt.Errorf("baseline router is missing workflow state")
		}
		if _, deterministic := cognitiveModeForMethod(state.Incident.DiagnosisMethod); deterministic {
			return "cognitive_intent", nil
		}
		switch state.Incident.DiagnosisMethod {
		case domain.DiagnosisMethodDirect, domain.DiagnosisMethodRAG, domain.DiagnosisMethodReAct:
			return "baseline_strategy", nil
		default:
			return "", fmt.Errorf("method %q is not an independent baseline", state.Incident.DiagnosisMethod)
		}
	}, map[string]bool{"cognitive_intent": true, "baseline_strategy": true})); err != nil {
		return nil, err
	}
	if err := graph.AddBranch("objective_arbitration", compose.NewGraphBranch(func(_ context.Context, state *WorkflowState) (string, error) {
		if state == nil || state.Incident == nil {
			return "", fmt.Errorf("baseline arbitration is missing workflow state")
		}
		if state.DiagnosisRuntime == nil {
			return "", fmt.Errorf("baseline diagnosis runtime state was lost before arbitration")
		}
		if !state.DiagnosisRuntime.Completed {
			return "query_compiler", nil
		}
		return "recovery_permission", nil
	}, map[string]bool{"query_compiler": true, "recovery_permission": true})); err != nil {
		return nil, err
	}
	if err := graph.AddBranch("recovery_permission", compose.NewGraphBranch(func(_ context.Context, state *WorkflowState) (string, error) {
		if state.Incident.Status == domain.StatusNeedsAttention {
			return compose.END, nil
		}
		return "constrained_recovery_agents", nil
	}, map[string]bool{compose.END: true, "constrained_recovery_agents": true})); err != nil {
		return nil, err
	}
	return graph, nil
}
