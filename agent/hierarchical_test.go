package agent

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/kubepilot-aiops/kubepilot/internal/domain"
	"github.com/kubepilot-aiops/kubepilot/internal/safety"
	"github.com/kubepilot-aiops/kubepilot/reasoning"
)

type hierarchicalDiagnosisModel struct {
	mu                    sync.Mutex
	alternativeSawPrimary bool
}

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

func (m *hierarchicalDiagnosisModel) Generate(_ context.Context, messages []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	system, user := messages[0].Content, messages[len(messages)-1].Content
	response := ""
	switch {
	case strings.Contains(system, "Create the bounded investigation plan"):
		response = `{"objective":"separate resource saturation from dependency failure","tasks":[{"id":"metric","source":"metric","question":"is CPU saturated"},{"id":"log","source":"log","question":"are errors consistent"},{"id":"trace","source":"trace","question":"where is latency introduced"},{"id":"topology","source":"topology","question":"is the workload unhealthy"}],"stop_conditions":["one grounded hypothesis passes"],"round_limit":2}`
	case strings.Contains(system, "Summarize the supplied evidence"):
		ids := modelPayloadEvidenceIDs(user)
		raw, _ := json.Marshal(map[string]any{"summary": "current server-owned evidence", "evidence_ids": ids, "supporting_hypothesis_ids": []string{}, "contradicting_hypothesis_ids": []string{}, "unknowns": []string{}})
		response = string(raw)
	case strings.Contains(system, "Form plausible alternative explanations"):
		m.mu.Lock()
		m.alternativeSawPrimary = m.alternativeSawPrimary || strings.Contains(user, "primary-only-cause")
		m.mu.Unlock()
		ids := modelPayloadEvidenceIDs(user)
		response = modelArgumentJSON("alternative", "network", "packet_loss", "independent network loss", .1, ids, []string{"packet loss"})
	case strings.Contains(system, "Determine the most defensible root cause"):
		ids := modelPayloadEvidenceIDs(user)
		response = modelArgumentJSON("primary", "cpu", "busy_loop", "primary-only-cause", 1, ids, []string{"cpu saturation"})
	case strings.Contains(system, "Challenge the competing hypotheses"):
		response = `{"critiques":[]}`
	default:
		response = `{}`
	}
	return &schema.Message{Role: schema.Assistant, Content: response, ResponseMeta: &schema.ResponseMeta{Usage: &schema.TokenUsage{PromptTokens: 40, CompletionTokens: 10, TotalTokens: 50}}}, nil
}

func (m *hierarchicalDiagnosisModel) Stream(ctx context.Context, messages []*schema.Message, options ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	message, err := m.Generate(ctx, messages, options...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{message}), nil
}

func modelPayloadEvidenceIDs(raw string) []string {
	var payload struct {
		Evidence []domain.Evidence `json:"evidence"`
	}
	_ = json.Unmarshal([]byte(raw), &payload)
	ids := make([]string, 0, len(payload.Evidence))
	for _, item := range payload.Evidence {
		ids = append(ids, item.ID)
	}
	return ids
}

func modelArgumentJSON(id, category, variant, cause string, prior float64, evidenceIDs, path []string) string {
	raw, _ := json.Marshal(map[string]any{
		"hypotheses": []map[string]any{{
			"id": id, "category": category, "variant": variant, "cause": cause,
			"service": "gateway-service", "resource": "gateway-service", "prior_probability": prior,
			"supporting_evidence_ids": evidenceIDs, "expected_causal_path": path,
			"falsification_conditions": []string{"current evidence no longer reproduces"},
		}},
		"evidence_ids": evidenceIDs,
		"uncertainty":  "bounded by current observations",
	})
	return string(raw)
}

func TestEvidenceWorkersRecordPartialInfrastructureFailure(t *testing.T) {
	registry := &AgentRegistry{}
	plan := domain.InvestigationPlan{Tasks: []domain.WorkerTask{{ID: "metric", Source: "metric"}, {ID: "topology", Source: "topology"}}}
	findings, evidence, usage, infrastructure := registry.runEvidenceWorkers(context.Background(), &domain.Incident{ID: "incident"}, plan, []string{"metric", "topology"}, map[string]Collector{}, nil)
	if len(findings) != 0 || len(evidence) != 0 || len(usage) != 0 || len(infrastructure) != 2 {
		t.Fatalf("partial worker failure was not isolated: findings=%+v evidence=%+v usage=%+v infrastructure=%+v", findings, evidence, usage, infrastructure)
	}
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

func TestInvestigationPlanRequiresTopologyAndOperationalSignal(t *testing.T) {
	plan, err := validateInvestigationPlan(plannerResponse{Tasks: []domain.WorkerTask{
		{Source: "metric"}, {Source: "kubernetes"}, {Source: "metric"}, {Source: "log"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if plan.RoundLimit != 2 || len(plan.Tasks) != 3 || plan.Tasks[1].Source != "topology" || !plan.Tasks[1].Required {
		t.Fatalf("plan was not normalized and bounded: %+v", plan)
	}
	if _, err = validateInvestigationPlan(plannerResponse{Tasks: []domain.WorkerTask{{Source: "metric"}, {Source: "log"}}}); err == nil {
		t.Fatal("plan without topology was accepted")
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

func TestAlternativeIDsCritiqueSourcesAndMemoryScopeAreDeterministic(t *testing.T) {
	primary := []domain.HypothesisDraft{{ID: "database", Cause: "mysql saturation"}}
	alternative := disambiguateAlternativeIDs(primary, []domain.HypothesisDraft{{ID: "database", Cause: "network timeout"}})
	if len(alternative) != 1 || alternative[0].ID != "alternative-database" {
		t.Fatalf("alternative hypothesis was not disambiguated: %+v", alternative)
	}
	sources := critiqueSources([]domain.Critique{{RecommendedSources: []string{"kubernetes", "trace", "TRACE", "invalid"}}})
	if len(sources) != 2 || sources[0] != "topology" || sources[1] != "trace" {
		t.Fatalf("critique source gaps were not normalized: %+v", sources)
	}
	patterns := causalPatternsForScope([]domain.CausalPattern{
		{ID: "global"},
		{ID: "matching", Cluster: "cluster-a", Namespace: "team-a"},
		{ID: "other-cluster", Cluster: "cluster-b", Namespace: "team-a"},
		{ID: "other-namespace", Cluster: "cluster-a", Namespace: "team-b"},
	}, "cluster-a", "team-a", 0)
	if len(patterns) != 2 || patterns[0].ID != "global" || patterns[1].ID != "matching" {
		t.Fatalf("causal memory crossed scope: %+v", patterns)
	}
}

func TestHierarchicalDiagnosisFiltersUngroundedModelDraftsBeforeVerification(t *testing.T) {
	evidence := []domain.Evidence{{ID: "metric", Source: "prometheus"}, {ID: "topology", Source: "kubernetes"}}
	valid := domain.HypothesisDraft{ID: "valid", SupportingEvidenceIDs: []string{"metric", "topology"}, ExpectedCausalPath: []string{"resource pressure", "latency"}}
	drafts := []domain.HypothesisDraft{
		valid,
		{ID: "missing-support", ExpectedCausalPath: []string{"latency"}},
		{ID: "unknown-evidence", SupportingEvidenceIDs: []string{"not-in-ledger"}, ExpectedCausalPath: []string{"latency"}},
		{ID: "missing-path", SupportingEvidenceIDs: []string{"metric"}},
		{ID: "unknown-contradiction", SupportingEvidenceIDs: []string{"metric"}, ContradictingEvidenceIDs: []string{"not-in-ledger"}, ExpectedCausalPath: []string{"latency"}},
	}
	grounded := filterGroundedHypothesisDrafts(drafts, evidence)
	if len(grounded) != 1 || grounded[0].ID != valid.ID {
		t.Fatalf("ungrounded model drafts reached verification: %+v", grounded)
	}
	if _, err := reasoning.New(reasoning.DefaultConfig()).VerifyHypotheses(grounded, evidence, nil, nil); err != nil {
		t.Fatalf("server-filtered drafts did not verify: %v", err)
	}
}

func TestDecodePlannerResponseAcceptsKnownProviderEnvelopesOnly(t *testing.T) {
	for _, raw := range []string{
		`{"plan":{"objective":"diagnose","tasks":[{"id":"metric","source":"metric","question":"cpu?","required":true},{"id":"topology","source":"topology","question":"ready?","required":true}],"stop_conditions":["grounded"],"round_limit":2},"round":1}`,
		`{"investigation_plan":{"objective":"diagnose","worker_tasks":[{"id":"metric","source":"metric","question":"cpu?","required":true},{"id":"topology","source":"topology","question":"ready?","required":true}],"stop_conditions":["grounded"]}}`,
	} {
		var response plannerResponse
		if err := decodePlannerResponse(raw, &response); err != nil {
			t.Fatal(err)
		}
		if len(response.Tasks) != 2 || response.RoundLimit != 2 {
			t.Fatalf("normalized response=%+v", response)
		}
	}
	var rejected plannerResponse
	if err := decodePlannerResponse(`{"objective":"diagnose","tasks":[],"unsafe_override":true}`, &rejected); err == nil {
		t.Fatal("unknown planner field was accepted")
	}
}

func TestCausalModesSelectOnlyTheRequestedKnowledge(t *testing.T) {
	patterns := []domain.CausalPattern{{ID: "static", Source: "builtin"}, {ID: "learned", Source: "learned"}}
	if got := causalPatternsForMode(patterns, domain.CausalModeNone); len(got) != 0 {
		t.Fatalf("no-causal retained patterns: %+v", got)
	}
	if got := causalPatternsForMode(patterns, domain.CausalModeStatic); len(got) != 1 || got[0].ID != "static" {
		t.Fatalf("static-causal selected the wrong patterns: %+v", got)
	}
	if got := causalPatternsForMode(patterns, domain.CausalModeLearned); len(got) != 1 || got[0].ID != "learned" {
		t.Fatalf("learned-causal selected the wrong patterns: %+v", got)
	}
	if got := causalPatternsForMode(patterns, domain.CausalModeFull); len(got) != 2 {
		t.Fatalf("full causal mode omitted patterns: %+v", got)
	}
}

func TestHierarchicalDiagnosisExecutesPlanWorkersBlindDebateAndArbitration(t *testing.T) {
	chat := &hierarchicalDiagnosisModel{}
	registry, err := NewAgentRegistry(context.Background(), chat)
	if err != nil {
		t.Fatal(err)
	}
	collectors := map[string]Collector{
		"metric":     strategyCollector{source: "prometheus", evidenceType: "cpu_metric"},
		"log":        strategyCollector{source: "loki", evidenceType: "error_log"},
		"trace":      strategyCollector{source: "jaeger", evidenceType: "trace"},
		"kubernetes": strategyCollector{source: "kubernetes", evidenceType: "kubernetes_event"},
	}
	incident := &domain.Incident{ID: "hierarchical-incident", Cluster: "cluster-a", Namespace: "kubepilot-demo", Service: "gateway-service", Resource: "gateway-service", Summary: "CPU saturation and request latency", CausalMode: domain.CausalModeNone, CreatedAt: time.Now().Add(-time.Minute)}
	result, err := registry.runHierarchicalDiagnosis(context.Background(), incident, constrainedToolDeps{Collectors: collectors, Historical: fixedHistoricalRetriever{}, Reasoning: reasoning.New(reasoning.DefaultConfig()), Memory: &strategyMemoryRecorder{}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Investigation == nil || len(result.Investigation.Plan.Tasks) != 4 || len(result.Investigation.Findings) < 4 || len(result.Investigation.Debate) < 1 || len(result.Investigation.Debate) > 2 || len(result.Investigation.MemoryReads) != 3 {
		t.Fatalf("hierarchical execution was incomplete: %+v", result.Investigation)
	}
	if result.Investigation.Arbitration == nil || !result.Investigation.Arbitration.Accepted || result.SelectedHypothesisID != "primary" {
		t.Fatalf("deterministic arbitration did not converge: %+v", result.Investigation.Arbitration)
	}
	chat.mu.Lock()
	alternativeSawPrimary := chat.alternativeSawPrimary
	chat.mu.Unlock()
	if alternativeSawPrimary {
		t.Fatal("alternative agent received the primary conclusion in its first-round input")
	}
	if incident.AgentBudget == nil {
		t.Fatal("hierarchical diagnosis did not persist Agent budgets")
	}
	for _, name := range diagnosisAgentNames() {
		usage := incident.AgentBudget.Usage[name]
		if usage.Iterations == 0 {
			t.Fatalf("%s usage was not charged independently: %+v", name, incident.AgentBudget)
		}
		if incident.AgentBudget.Limits[name].MaxTokens != 8192 {
			t.Fatalf("%s token budget=%d, want 8192", name, incident.AgentBudget.Limits[name].MaxTokens)
		}
	}
}

func TestHierarchicalBudgetChargesGeneratedOutputOnly(t *testing.T) {
	budget := domain.AgentBudget{MaxIterations: 1, MaxTokens: 8192}
	controller := safety.NewBudgetController(nil, map[string]domain.AgentBudget{PlannerAgentName: budget}, nil)
	usage := domain.ModelUsageEvent{Agent: PlannerAgentName, InputTokens: 50000, OutputTokens: 8192}
	if err := chargeAgentUsage(controller, usage); err != nil {
		t.Fatalf("input tokens were incorrectly charged against the output budget: %v", err)
	}
	state := controller.State()
	if state.Usage[PlannerAgentName].Tokens != 8192 || state.IncidentTokens != 8192 {
		t.Fatalf("output-token accounting mismatch: %+v", state)
	}
}
