package agent

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	workflowgraph "github.com/kubepilot-aiops/kubepilot/graph"
	"github.com/kubepilot-aiops/kubepilot/internal/domain"
	captools "github.com/kubepilot-aiops/kubepilot/tools"
)

type correlationModel struct{}

func (correlationModel) Generate(_ context.Context, messages []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	available := map[string]bool{}
	for _, candidate := range model.GetCommonOptions(nil, opts...).Tools {
		available[candidate.Name] = true
	}
	called := calledTools(messages)
	name := "query_incident_candidates"
	arguments := map[string]any{"limit": 100}
	if called[name] {
		name = "submit_correlation_decision"
		arguments = map[string]any{"merge": true, "incident_id": "incident-existing", "confidence": .92, "reason": "same namespace, service and active symptom window"}
	}
	if !available[name] {
		return schema.AssistantMessage("required correlation capability unavailable", nil), nil
	}
	raw, _ := json.Marshal(arguments)
	return withMockUsage(&schema.Message{Role: schema.Assistant, ToolCalls: []schema.ToolCall{{ID: "correlation-" + name, Type: "function", Function: schema.FunctionCall{Name: name, Arguments: string(raw)}}}}), nil
}

func (m correlationModel) Stream(ctx context.Context, messages []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	message, err := m.Generate(ctx, messages, opts...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{message}), nil
}

func TestAgentRegistryCorrelationUsesBoundedCandidateCapability(t *testing.T) {
	registry, err := NewAgentRegistry(context.Background(), correlationModel{})
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := captools.NewCapability("query_incident_candidates", "Return active incidents from the authoritative namespace.", func(_ context.Context, input boundedLimit) (map[string]any, error) {
		if input.Limit != 100 {
			t.Fatalf("candidate limit=%d", input.Limit)
		}
		return map[string]any{"incidents": []string{"incident-existing"}}, nil
	}, captools.Registration{Category: captools.CategoryRetrieval, AllowedNodes: []string{captools.NodeAlertCorrelation}, Timeout: time.Second, MaxArgumentBytes: 1024, MaxOutputBytes: 4096})
	if err != nil {
		t.Fatal(err)
	}
	incidentID, err := registry.CorrelateWithCandidateCapability(context.Background(), domain.Alert{Name: "HighLatency", StartsAt: time.Now().UTC(), Labels: map[string]string{"service": "payment-service"}}, "payment-service", "kubepilot-demo", "payment-service", candidate)
	if err != nil || incidentID != "incident-existing" {
		t.Fatalf("correlation decision=%q err=%v", incidentID, err)
	}
	if _, err = registry.CorrelateWithCandidateCapability(context.Background(), domain.Alert{}, "payment-service", "kubepilot-demo", "payment-service", nil); err == nil {
		t.Fatal("nil candidate capability was accepted")
	}
}

func TestRegistryProceduralMemoryAndSupervisorRuntimeMetadata(t *testing.T) {
	registry, err := NewAgentRegistry(context.Background(), correlationModel{})
	if err != nil {
		t.Fatal(err)
	}
	memories := registry.ProceduralMemories()
	if len(memories) != len(registry.Names()) {
		t.Fatalf("procedural memories=%d roles=%d", len(memories), len(registry.Names()))
	}
	for _, memory := range memories {
		if memory.Kind != domain.MemoryProcedural || len(memory.Version) != 64 || memory.Summary == "" {
			t.Fatalf("invalid procedural memory: %+v", memory)
		}
	}

	supervisor := &Supervisor{skillSnapshotHash: "skills", brainSkillHash: "brain-skills", rankingPolicyHash: "ranking", hooks: &supervisorHooks{}}
	sink := workflowgraph.EventSink(func(context.Context, workflowgraph.WorkflowEvent) {})
	supervisor.SetEventSink(sink)
	if supervisor.eventSink == nil || supervisor.hooks.eventSink == nil {
		t.Fatal("event sink was not propagated to runtime hooks")
	}
	skillHash, rankingHash, rerankerHash := supervisor.RuntimeHashes()
	if skillHash != "skills" || rankingHash != "ranking" || rerankerHash != "" {
		t.Fatalf("unexpected runtime hashes: %q %q %q", skillHash, rankingHash, rerankerHash)
	}
	brainSkillHash, brainRankingHash, brainRerankerHash := supervisor.RuntimeHashesForMethod(domain.DiagnosisMethodKubePilot)
	if brainSkillHash != "brain-skills" || brainRankingHash != "ranking" || brainRerankerHash != "" {
		t.Fatalf("unexpected Brain runtime hashes: %q %q %q", brainSkillHash, brainRankingHash, brainRerankerHash)
	}
}

func TestSmallDeterministicAgentHelpers(t *testing.T) {
	if got := firstNonBlank("", "  root cause ", "fallback"); got != "root cause" {
		t.Fatalf("first non-blank=%q", got)
	}
	if got := firstNonBlank(" "); got != "unspecified" {
		t.Fatalf("empty fallback=%q", got)
	}
	terms := nonEmptyTerms([]string{" redis ", "", "redis", "network"})
	if len(terms) != 2 || terms[0] != "redis" || terms[1] != "network" {
		t.Fatalf("terms=%v", terms)
	}
	got := appendUnique([]string{"metric"}, "metric")
	got = appendUnique(got, "")
	got = appendUnique(got, "log")
	if len(got) != 3 || got[1] != "" || got[2] != "log" {
		t.Fatalf("appendUnique=%v", got)
	}
}
