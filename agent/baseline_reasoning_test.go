package agent

import (
	"context"
	"testing"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/kubepilot-aiops/kubepilot/internal/domain"
	"github.com/kubepilot-aiops/kubepilot/reasoning"
)

type visibleJSONRetryModel struct{ calls int }

func (m *visibleJSONRetryModel) Generate(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	m.calls++
	if m.calls == 1 {
		return &schema.Message{Role: schema.Assistant, ReasoningContent: "hidden provider reasoning", ResponseMeta: &schema.ResponseMeta{Usage: &schema.TokenUsage{PromptTokens: 11, CompletionTokens: 8192, TotalTokens: 8203, CompletionTokensDetails: schema.CompletionTokensDetails{ReasoningTokens: 8192}}}}, nil
	}
	return &schema.Message{Role: schema.Assistant, Content: `{}`, ResponseMeta: &schema.ResponseMeta{Usage: &schema.TokenUsage{PromptTokens: 13, CompletionTokens: 5, TotalTokens: 18}}}, nil
}

func (m *visibleJSONRetryModel) Stream(ctx context.Context, messages []*schema.Message, options ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	message, err := m.Generate(ctx, messages, options...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{message}), nil
}

func TestGenerateRoleRetriesMissingVisibleJSONAndAccountsBothAttempts(t *testing.T) {
	chat := &visibleJSONRetryModel{}
	registry := &AgentRegistry{chat: chat, skills: map[string]agentSkill{PlannerAgentName: {Content: "planner skill"}}}
	message, err := registry.generateRole(context.Background(), PlannerAgentName, "Return a JSON object.", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	if chat.calls != 2 || message == nil || message.Content != `{}` {
		t.Fatalf("visible JSON retry did not complete: calls=%d message=%+v", chat.calls, message)
	}
	usage := registry.modelUsage("incident", PlannerAgentName, message, time.Second)
	if usage.InputTokens != 24 || usage.OutputTokens != 8197 || usage.ReasoningTokens != 8192 {
		t.Fatalf("retry usage was not aggregated: %+v", usage)
	}
}

func TestDeterministicArbiterRequiresConfidenceEvidenceAndMargin(t *testing.T) {
	evidence := []domain.Evidence{{ID: "kube", Source: "kubernetes"}, {ID: "metric", Source: "prometheus"}}
	best := domain.VerifiedHypothesis{Draft: domain.HypothesisDraft{ID: "best"}, Status: domain.HypothesisSupported, FinalScore: .90, ContradictionScore: .05, VerifiedEvidenceIDs: []string{"kube", "metric"}}
	second := domain.VerifiedHypothesis{Draft: domain.HypothesisDraft{ID: "second"}, Status: domain.HypothesisSupported, FinalScore: .70, VerifiedEvidenceIDs: []string{"kube", "metric"}}
	accepted := arbitrateHypotheses([]domain.VerifiedHypothesis{second, best}, evidence)
	if !accepted.Accepted || accepted.SelectedHypothesisID != "best" || accepted.ScoreMargin < .15 {
		t.Fatalf("grounded hypothesis was not accepted: %+v", accepted)
	}
	second.FinalScore = .80
	unresolved := arbitrateHypotheses([]domain.VerifiedHypothesis{best, second}, evidence)
	if unresolved.Accepted || !unresolved.NeedsMoreEvidence {
		t.Fatalf("small-margin debate was accepted: %+v", unresolved)
	}
	best.VerifiedEvidenceIDs = []string{"metric"}
	ungrounded := arbitrateHypotheses([]domain.VerifiedHypothesis{best}, evidence)
	if ungrounded.Accepted {
		t.Fatalf("single-source hypothesis was accepted: %+v", ungrounded)
	}
}

func TestBaselineFiltersUngroundedModelDraftsBeforeVerification(t *testing.T) {
	evidence := []domain.Evidence{{ID: "metric", Source: "prometheus", CausalNodeIDs: []string{"obs:metric"}}, {ID: "topology", Source: "kubernetes", CausalNodeIDs: []string{"obs:topology"}}}
	valid := domain.HypothesisDraft{ID: "valid", SupportingEvidenceIDs: []string{"metric", "topology"}, ExpectedCausalNodeIDs: []string{"obs:metric"}}
	drafts := []domain.HypothesisDraft{
		valid,
		{ID: "missing-support", ExpectedCausalNodeIDs: []string{"obs:metric"}},
		{ID: "unknown-evidence", SupportingEvidenceIDs: []string{"not-in-ledger"}, ExpectedCausalNodeIDs: []string{"obs:metric"}},
		{ID: "missing-path", SupportingEvidenceIDs: []string{"metric"}},
		{ID: "unknown-contradiction", SupportingEvidenceIDs: []string{"metric"}, ContradictingEvidenceIDs: []string{"not-in-ledger"}, ExpectedCausalNodeIDs: []string{"obs:metric"}},
	}
	grounded := filterGroundedHypothesisDrafts(drafts, evidence)
	if len(grounded) != 1 || grounded[0].ID != valid.ID {
		t.Fatalf("ungrounded model drafts reached verification: %+v", grounded)
	}
	if _, err := reasoning.New(reasoning.DefaultConfig()).VerifyHypotheses(grounded, evidence, nil, nil); err != nil {
		t.Fatalf("server-filtered drafts did not verify: %v", err)
	}
}

func TestCausalAllowlistOnlyExposesObservedPatternGraph(t *testing.T) {
	patterns := []domain.CausalPattern{{
		ID: "cpu", Status: "active",
		Nodes: []domain.CausalNode{{ID: "cpu_demand"}, {ID: "cpu_saturation"}, {ID: "latency_error"}},
		Edges: []domain.CausalEdge{{From: "cpu_demand", To: "cpu_saturation"}, {From: "cpu_saturation", To: "latency_error"}},
	}}
	evidence := []domain.Evidence{
		{ID: "metric", CausalNodeIDs: []string{"obs:metric", "cpu_demand", "signal:prometheus:cpu"}},
		{ID: "trace", CausalNodeIDs: []string{"obs:trace", "cpu_saturation"}},
	}
	nodes := causalNodeAllowlist(evidence, patterns)
	for _, nodeID := range []string{"obs:metric", "obs:trace", "cpu_demand", "cpu_saturation"} {
		if _, ok := nodes[nodeID]; !ok {
			t.Fatalf("observed node missing from allowlist: %s", nodeID)
		}
	}
	for _, nodeID := range []string{"signal:prometheus:cpu", "latency_error"} {
		if _, ok := nodes[nodeID]; ok {
			t.Fatalf("unobserved or non-graph node exposed to model: %s", nodeID)
		}
	}
	edges := causalEdgeAllowlist(evidence, patterns)
	if !causalPathIsServerValid([]string{"cpu_demand", "cpu_saturation"}, []string{"metric", "trace"}, edges) {
		t.Fatal("observed directed graph path was rejected")
	}
	if causalPathIsServerValid([]string{"cpu_saturation", "cpu_demand"}, []string{"metric", "trace"}, edges) {
		t.Fatal("reversed graph path was accepted")
	}
}
