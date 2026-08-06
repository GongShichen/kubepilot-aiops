package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/cloudwego/eino/components/tool"
	"github.com/kubepilot-aiops/kubepilot/internal/causal"
	causaldiscovery "github.com/kubepilot-aiops/kubepilot/internal/causal/discovery"
	causalknowledge "github.com/kubepilot-aiops/kubepilot/internal/causal/knowledge"
	"github.com/kubepilot-aiops/kubepilot/internal/domain"
	"github.com/kubepilot-aiops/kubepilot/internal/retrieval/reranker"
	"github.com/kubepilot-aiops/kubepilot/internal/safety"
	topologyknowledge "github.com/kubepilot-aiops/kubepilot/internal/topology/knowledge"
	"github.com/kubepilot-aiops/kubepilot/reasoning"
	captools "github.com/kubepilot-aiops/kubepilot/tools"
)

type deterministicCapabilityReranker struct{}

func (deterministicCapabilityReranker) Enabled() bool { return true }
func (deterministicCapabilityReranker) Rerank(_ context.Context, _ string, documents []string, _ int) ([]reranker.Result, error) {
	results := make([]reranker.Result, 0, len(documents))
	for index := range documents {
		results = append(results, reranker.Result{Index: index, Score: 1})
	}
	return results, nil
}
func (deterministicCapabilityReranker) Probe(context.Context) error { return nil }
func (deterministicCapabilityReranker) ConfigHash() string          { return "deterministic-reranker" }
func (deterministicCapabilityReranker) Health() map[string]any {
	return map[string]any{"configured": true}
}

type memoryIncidentRetriever struct{}

func (memoryIncidentRetriever) candidate(features domain.IncidentFeatures) domain.RetrievalCandidate {
	return domain.RetrievalCandidate{IncidentID: "resolved-memory-incident", Namespace: features.Namespace, Service: features.Service, Resource: features.Resource, Category: "memory", RootCause: "memory leak", Features: domain.IncidentFeatures{Namespace: features.Namespace, Service: features.Service, Resource: features.Resource, Terms: features.Terms, TopologyServices: features.TopologyServices}, SourceScores: map[string]float64{"semantic": .95, "lexical": .9, "topology": .9}}
}
func (r memoryIncidentRetriever) Semantic(_ context.Context, features domain.IncidentFeatures, _ int) ([]domain.RetrievalCandidate, error) {
	return []domain.RetrievalCandidate{r.candidate(features)}, nil
}
func (r memoryIncidentRetriever) Lexical(_ context.Context, features domain.IncidentFeatures, _ int) ([]domain.RetrievalCandidate, error) {
	return []domain.RetrievalCandidate{r.candidate(features)}, nil
}
func (r memoryIncidentRetriever) Topology(_ context.Context, features domain.IncidentFeatures, _ int) ([]domain.RetrievalCandidate, error) {
	return []domain.RetrievalCandidate{r.candidate(features)}, nil
}

func TestDiagnosisCapabilitiesMaintainOneAuditableWorkflowState(t *testing.T) {
	topologyStore := topologyknowledge.NewMemoryStore()
	causalStore := causalknowledge.NewMemoryStore()
	discoveredStore := causaldiscovery.NewMemoryStore()
	if err := discoveredStore.Upsert(context.Background(), causaldiscovery.CausalPatternCandidate{CausalPath: []string{"memory_growth", "oom_killed", "pod_restart"}, Status: causaldiscovery.StatusAccepted, Frequency: 3, Coverage: 1, Confidence: .9}); err != nil {
		t.Fatal(err)
	}
	dependencies := constrainedToolDeps{
		Historical:         memoryIncidentRetriever{},
		Reasoning:          reasoning.New(reasoning.DefaultConfig()),
		Reranker:           deterministicCapabilityReranker{},
		Causal:             causal.DefaultMatcher(),
		TopologyPatterns:   topologyStore,
		CausalPatterns:     causalStore,
		DiscoveredPatterns: discoveredStore,
	}
	capabilities, err := buildConstrainedDiagnosisCapabilities(dependencies)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]tool.InvokableTool{}
	for _, candidate := range registeredCapabilitiesForTest(t, captools.NodeDiagnosisReact, capabilities) {
		info, infoErr := candidate.Info(context.Background())
		if infoErr != nil {
			t.Fatal(infoErr)
		}
		if invokable, ok := candidate.(tool.InvokableTool); ok {
			byName[info.Name] = invokable
		}
	}
	now := time.Now().UTC()
	incident := &domain.Incident{
		ID: "capability-contract", Status: domain.StatusDiagnosing, Cluster: "cluster-a", Namespace: "kubepilot-demo", Service: "payment-service", Resource: "payment-service", Summary: "payment latency after memory growth", CreatedAt: now.Add(-time.Minute), EvidenceStartAt: now.Add(-5 * time.Minute), Confidence: .9,
		Evidence: []domain.Evidence{
			{ID: "metric-memory", Source: "prometheus", Type: "memory_metric", Summary: "memory growth above limit", Timestamp: now, Namespace: "kubepilot-demo", Service: "payment-service", Resource: "payment-service", Confidence: .95, Content: map[string]any{"result": "memory_growth"}},
			{ID: "event-oom", Source: "kubernetes", Type: "kubernetes_event", Summary: "OOMKilled pod restart", Timestamp: now, Namespace: "kubepilot-demo", Service: "payment-service", Resource: "payment-service", Confidence: .95},
			{ID: "log-error", Source: "loki", Type: "log_error", Summary: "request error rate increase", Timestamp: now, Namespace: "kubepilot-demo", Service: "payment-service", Resource: "payment-service", Confidence: .9},
			{ID: "trace-timeout", Source: "jaeger", Type: "trace_error", Summary: "payment request timeout", Timestamp: now, Namespace: "kubepilot-demo", Service: "payment-service", Resource: "payment-service", Confidence: .9},
		},
		AgentBudget: &domain.AgentBudgetState{},
	}
	budget := safety.NewBudgetController(incident.AgentBudget, map[string]domain.AgentBudget{DiagnosisAgentName: {MaxIterations: 20, MaxToolUses: 40, MaxTokens: 30000, MaxCorrections: 5}}, map[string]int{})
	state := &WorkflowState{Workflow: WorkflowName, Incident: incident}
	runtime := &constrainedRuntime{state: state, budgets: budget, done: map[string]bool{}}
	ctx := withConstrainedRuntime(context.Background(), runtime)

	invoke := func(name string, input any) constrainedToolOutput {
		t.Helper()
		candidate := byName[name]
		if candidate == nil {
			t.Fatalf("missing capability %s", name)
		}
		payload, marshalErr := json.Marshal(input)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		raw, callErr := candidate.InvokableRun(ctx, string(payload))
		if callErr != nil {
			t.Fatalf("%s: %v", name, callErr)
		}
		var output constrainedToolOutput
		if decodeErr := json.Unmarshal([]byte(raw), &output); decodeErr != nil {
			t.Fatalf("%s returned invalid JSON: %v (%s)", name, decodeErr, raw)
		}
		return output
	}

	for _, name := range []string{"query_prometheus_evidence", "query_loki_evidence", "query_trace_evidence", "query_kubernetes_evidence"} {
		if output := invoke(name, investigationRequest{WindowMinutes: 5}); !output.OK || output.Message != "persisted evidence reused" {
			t.Fatalf("%s did not reuse authoritative evidence: %+v", name, output)
		}
	}
	for _, call := range []struct {
		name  string
		input any
	}{
		{name: "rank_incident_evidence", input: emptyToolInput{}},
		{name: "rerank_incident_evidence", input: emptyToolInput{}},
		{name: "build_incident_features", input: emptyToolInput{}},
		{name: "build_incident_graph", input: graphBuildRequest{}},
		{name: "retrieve_topology_patterns", input: boundedLimit{Limit: 100}},
		{name: "retrieve_causal_patterns", input: boundedLimit{Limit: 100}},
		{name: "retrieve_discovered_causal_patterns", input: boundedLimit{Limit: 100}},
		{name: "retrieve_semantic_incidents", input: boundedLimit{Limit: 10}},
		{name: "retrieve_lexical_incidents", input: boundedLimit{Limit: 10}},
		{name: "retrieve_topology_incidents", input: boundedLimit{Limit: 10}},
		{name: "fuse_incident_candidates", input: emptyToolInput{}},
		{name: "rerank_incident_candidates", input: emptyToolInput{}},
		{name: "match_causal_patterns", input: emptyToolInput{}},
		{name: "expand_causal_path", input: causalPathRequest{PatternID: "memory-leak"}},
		{name: "propose_causal_pattern", input: causalPatternProposalRequest{Cause: "memory leak", Path: []string{"memory_leak", "memory_growth", "oom_killed"}, EvidenceIDs: []string{"metric-memory", "event-oom"}}},
		{name: "validate_causal_pattern", input: emptyToolInput{}},
	} {
		output := invoke(call.name, call.input)
		if !output.OK {
			t.Fatalf("%s rejected valid workflow state: %+v", call.name, output)
		}
	}
	if output := invoke("expand_causal_path", causalPathRequest{PatternID: "unknown-pattern"}); output.Feedback == nil || output.Feedback.Code != "causal_pattern_not_found" || !output.Feedback.Retryable {
		t.Fatalf("unknown causal path did not produce repairable feedback: %+v", output)
	}

	hypothesis := domain.HypothesisDraft{ID: "memory-root", Category: "memory", Variant: "memory_leak", Cause: "memory leak", Service: incident.Service, Resource: incident.Resource, PriorProbability: 1, SupportingEvidenceIDs: []string{"metric-memory", "event-oom", "log-error"}, ExpectedCausalPath: []string{"memory_growth", "oom_killed", "pod_restart"}, FalsificationConditions: []string{"memory remains stable"}}
	if output := invoke("submit_hypotheses", HypothesisSubmission{ReasoningType: "hypothesis_verification", Hypotheses: []domain.HypothesisDraft{hypothesis}}); !output.OK {
		t.Fatalf("hypothesis submission failed: %+v", output)
	}
	if output := invoke("verify_incident_hypotheses", emptyToolInput{}); len(output.Verified) != 1 {
		t.Fatalf("verification output=%+v", output)
	}
	if output := invoke("score_hypothesis_causality", hypothesisSelection{HypothesisID: hypothesis.ID}); !output.OK || output.CausalScore == nil {
		t.Fatalf("causal scoring output=%+v", output)
	}
	diagnosisOutput := invoke("submit_diagnosis", hypothesisSelection{HypothesisID: hypothesis.ID})
	if diagnosisOutput.Feedback == nil || diagnosisOutput.Feedback.Code != "diagnosis_insufficient" || !diagnosisOutput.Feedback.Retryable {
		t.Fatalf("insufficient direct support did not produce repairable feedback: %+v", diagnosisOutput)
	}
	if state.IncidentGraph == nil || len(state.Candidates) == 0 || state.CausalProposal == nil || state.CausalValidation == nil || state.DiagnosisLedger.SelectedHypothesisID != "" {
		t.Fatalf("capability state is incomplete: %s diagnosis=%+v verified=%+v", fmt.Sprintf("graph=%v candidates=%d proposal=%v validation=%v selected=%q", state.IncidentGraph != nil, len(state.Candidates), state.CausalProposal != nil, state.CausalValidation != nil, state.DiagnosisLedger.SelectedHypothesisID), diagnosisOutput, state.VerifiedHypotheses)
	}
}
