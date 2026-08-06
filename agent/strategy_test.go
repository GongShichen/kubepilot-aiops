package agent

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/kubepilot-aiops/kubepilot/internal/domain"
	"github.com/kubepilot-aiops/kubepilot/reasoning"
)

type singlePassDiagnosisModel struct{}

func (singlePassDiagnosisModel) Generate(_ context.Context, messages []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	ids := modelPayloadEvidenceIDs(messages[len(messages)-1].Content)
	raw, _ := json.Marshal(map[string]any{
		"hypotheses": []map[string]any{{
			"id": "root", "category": "cpu", "variant": "busy_loop", "cause": "CPU saturation",
			"service": "gateway-service", "resource": "gateway-service", "prior_probability": 1.0,
			"description":             "Provider-added explanation that is not part of the server contract",
			"supporting_evidence_ids": ids, "expected_causal_path": []string{"CPU saturation", "timeout"},
			"falsification_conditions": []string{"CPU is normal"},
		}},
		"selected_hypothesis_id": "root",
	})
	return &schema.Message{Role: schema.Assistant, Content: string(raw), ResponseMeta: &schema.ResponseMeta{Usage: &schema.TokenUsage{PromptTokens: 50, CompletionTokens: 20, TotalTokens: 70}}}, nil
}

func (m singlePassDiagnosisModel) Stream(ctx context.Context, messages []*schema.Message, options ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	message, err := m.Generate(ctx, messages, options...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{message}), nil
}

type strategyRuntimeModel struct{}

func (strategyRuntimeModel) Generate(ctx context.Context, messages []*schema.Message, options ...model.Option) (*schema.Message, error) {
	if len(model.GetCommonOptions(nil, options...).Tools) == 0 {
		return (singlePassDiagnosisModel{}).Generate(ctx, messages, options...)
	}
	return (scriptedEinoModel{}).Generate(ctx, messages, options...)
}

func (m strategyRuntimeModel) Stream(ctx context.Context, messages []*schema.Message, options ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	message, err := m.Generate(ctx, messages, options...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{message}), nil
}

type strategyMemoryRecorder struct{ events []domain.MemoryAccessEvent }

type strategyCollector struct {
	source, evidenceType string
}

func (collector strategyCollector) Collect(_ context.Context, incident *domain.Incident) ([]domain.Evidence, error) {
	return []domain.Evidence{{ID: incident.ID + "-" + collector.source, Source: collector.source, Type: collector.evidenceType, Summary: incident.Service + " CPU saturation throttling timeout error", Content: map[string]any{"level": "error", "result": "throttling timeout error"}, Timestamp: time.Now().UTC(), Namespace: incident.Namespace, Service: incident.Service, Resource: incident.Resource, Confidence: .9}}, nil
}

func (*strategyMemoryRecorder) Read(context.Context, domain.MemoryQuery) ([]domain.MemoryResult, error) {
	return nil, nil
}
func (*strategyMemoryRecorder) WriteVerifiedIncident(context.Context, domain.IncidentLearningInput) error {
	return nil
}
func (recorder *strategyMemoryRecorder) RecordAccess(_ context.Context, event domain.MemoryAccessEvent) error {
	recorder.events = append(recorder.events, event)
	return nil
}

func TestDirectAndRAGUseDistinctProductionFootprints(t *testing.T) {
	registry := &AgentRegistry{chat: singlePassDiagnosisModel{}}
	collectors := map[string]Collector{
		"metric":     strategyCollector{source: "prometheus", evidenceType: "cpu_metric"},
		"log":        strategyCollector{source: "loki", evidenceType: "error_log"},
		"trace":      strategyCollector{source: "jaeger", evidenceType: "trace"},
		"kubernetes": strategyCollector{source: "kubernetes", evidenceType: "kubernetes_event"},
	}
	memory := &strategyMemoryRecorder{}
	dependencies := constrainedToolDeps{Collectors: collectors, Historical: fixedHistoricalRetriever{}, Reasoning: reasoning.New(reasoning.DefaultConfig()), Memory: memory}
	directIncident := &domain.Incident{ID: "direct-incident", Cluster: "cluster-a", Namespace: "kubepilot-demo", Service: "gateway-service", Resource: "gateway-service"}
	direct, err := registry.runSinglePass(context.Background(), directIncident, dependencies, domain.DiagnosisMethodDirect)
	if err != nil {
		t.Fatal(err)
	}
	if direct.Investigation.Architecture != "single-pass" || len(direct.Candidates) != 0 || len(direct.Investigation.MemoryReads) != 0 || len(memory.events) != 0 {
		t.Fatalf("direct strategy crossed its fixed-evidence boundary: %+v", direct)
	}
	if id := (singlePassStrategy{id: domain.DiagnosisMethodDirect, registry: registry}).ID(); id != domain.DiagnosisMethodDirect {
		t.Fatalf("strategy ID=%s", id)
	}
	ragIncident := &domain.Incident{ID: "rag-incident", Cluster: "cluster-a", Namespace: "kubepilot-demo", Service: "gateway-service", Resource: "gateway-service"}
	rag, err := registry.runSinglePass(context.Background(), ragIncident, dependencies, domain.DiagnosisMethodRAG)
	if err != nil {
		t.Fatal(err)
	}
	if rag.Investigation.Architecture != "single-pass-episodic" || len(rag.Candidates) == 0 || len(rag.Investigation.MemoryReads) != 1 || len(memory.events) != 1 || memory.events[0].Kind != domain.MemoryEpisodic {
		t.Fatalf("RAG strategy did not use exactly one episodic read: result=%+v events=%+v", rag, memory.events)
	}
}

func TestApplySinglePassDiagnosisUsesCommonDeterministicHandoff(t *testing.T) {
	registry, err := NewAgentRegistry(context.Background(), singlePassDiagnosisModel{})
	if err != nil {
		t.Fatal(err)
	}
	collectors := map[string]Collector{
		"metric":     strategyCollector{source: "prometheus", evidenceType: "cpu_metric"},
		"log":        strategyCollector{source: "loki", evidenceType: "error_log"},
		"trace":      strategyCollector{source: "jaeger", evidenceType: "trace"},
		"kubernetes": strategyCollector{source: "kubernetes", evidenceType: "kubernetes_event"},
	}
	incident := &domain.Incident{ID: "direct-handoff", Status: domain.StatusDiagnosing, Namespace: "kubepilot-demo", Service: "gateway-service", Resource: "gateway-service", DiagnosisMethod: domain.DiagnosisMethodDirect, CreatedAt: time.Now()}
	engine := reasoning.New(reasoning.DefaultConfig())
	deps := constrainedToolDeps{Collectors: collectors, Reasoning: engine}
	result, err := registry.runSinglePass(context.Background(), incident, deps, domain.DiagnosisMethodDirect)
	if err != nil {
		t.Fatal(err)
	}
	state := &WorkflowState{Workflow: WorkflowName, Incident: incident}
	if err = registry.applyDiagnosisResult(context.Background(), state, deps, result); err != nil {
		t.Fatal(err)
	}
	if incident.Status != domain.StatusProposing || incident.RootCause != "CPU saturation" || incident.Confidence < .80 || state.DiagnosisLedger.SelectedHypothesisID != "root" {
		t.Fatalf("single-pass result did not enter the shared recovery handoff: incident=%+v ledger=%+v", incident, state.DiagnosisLedger)
	}
}

func TestStrategyDispatchRejectsInvalidStateAndReactRemovesEnhancements(t *testing.T) {
	registry := &AgentRegistry{}
	if err := registry.RunConstrained(context.Background(), nil, constrainedToolDeps{}); err == nil {
		t.Fatal("nil workflow state was accepted")
	}
	state := &WorkflowState{Incident: &domain.Incident{DiagnosisMethod: "unsupported"}}
	if err := registry.RunConstrained(context.Background(), state, constrainedToolDeps{}); err == nil {
		t.Fatal("unsupported strategy was accepted")
	}
	memory := &strategyMemoryRecorder{}
	collectors := map[string]Collector{"metric": strategyCollector{source: "prometheus"}}
	stripped := reactDependencies(constrainedToolDeps{Collectors: collectors, Historical: fixedHistoricalRetriever{}, Memory: memory})
	if stripped.Historical != nil || stripped.Memory != nil || stripped.Collectors["metric"] == nil {
		t.Fatalf("ReAct dependency boundary=%+v", stripped)
	}
}

func TestDirectStrategyDispatchContinuesThroughSharedRecoveryBoundary(t *testing.T) {
	registry, err := NewAgentRegistry(context.Background(), strategyRuntimeModel{})
	if err != nil {
		t.Fatal(err)
	}
	collectors := map[string]Collector{
		"metric":     strategyCollector{source: "prometheus", evidenceType: "cpu_metric"},
		"log":        strategyCollector{source: "loki", evidenceType: "error_log"},
		"trace":      strategyCollector{source: "jaeger", evidenceType: "trace"},
		"kubernetes": strategyCollector{source: "kubernetes", evidenceType: "kubernetes_event"},
	}
	incident := &domain.Incident{ID: "direct-runtime", Status: domain.StatusDiagnosing, Namespace: "kubepilot-demo", Service: "gateway-service", Resource: "gateway-service", DiagnosisMethod: domain.DiagnosisMethodDirect, CreatedAt: time.Now().UTC(), AgentBudget: &domain.AgentBudgetState{}}
	state := &WorkflowState{Workflow: WorkflowName, Incident: incident}
	executor := &graphExecutor{}
	deps := constrainedToolDeps{Collectors: collectors, Reasoning: reasoning.New(reasoning.DefaultConfig()), Executor: executor}
	if err = registry.RunConstrained(context.Background(), state, deps); err != nil {
		t.Fatal(err)
	}
	if incident.DiagnosisMethod != domain.DiagnosisMethodDirect || incident.Investigation == nil || incident.Investigation.Architecture != "single-pass" || incident.Proposal == nil || state.DryRun == nil || !state.DryRun.Success {
		t.Fatalf("direct dispatch did not reach common recovery boundary: incident=%+v state=%+v", incident, state)
	}
}
