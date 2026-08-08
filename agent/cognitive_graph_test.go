package agent

import (
	"context"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/eino/compose"
	"github.com/kubepilot-aiops/kubepilot/internal/domain"
	"github.com/kubepilot-aiops/kubepilot/internal/safety"
	"github.com/kubepilot-aiops/kubepilot/reasoning"
)

type cognitiveFixtureCollector struct{ source string }

type cognitivePatternReader struct{ patterns []domain.CausalPattern }

func (r cognitivePatternReader) ListCausalPatterns(_ context.Context, status string) ([]domain.CausalPattern, error) {
	var out []domain.CausalPattern
	for _, pattern := range r.patterns {
		if status == "" || pattern.Status == status {
			out = append(out, pattern)
		}
	}
	return out, nil
}

func (c cognitiveFixtureCollector) Collect(_ context.Context, incident *domain.Incident, _ domain.EvidenceRequest) ([]domain.Evidence, error) {
	now := time.Now().UTC()
	item := domain.Evidence{Source: c.source, Namespace: incident.Namespace, Service: incident.Service, Resource: incident.Resource, ObservedAt: now}
	switch c.source {
	case "prometheus":
		item.Kind, item.Summary, item.Facts = "cpu", "high CPU pressure", map[string]any{"current_value": .99, "baseline_value": .20}
	case "loki":
		item.Type, item.Summary, item.Facts = "log_entry", "request timeout", map[string]any{"level": "error", "occurrence_count": 8}
	case "jaeger":
		item.Kind, item.Summary, item.Facts = "trace", "slow request", map[string]any{"duration_micros": 1_000_000}
	default:
		item.Kind, item.Summary, item.Facts = "workload_state", "unready workload", map[string]any{"pods": []any{map[string]any{"ready": false, "restart_count": 2}}}
	}
	return []domain.Evidence{item}, nil
}

func runDeterministicBaselineGraphForTest(t *testing.T, registry *AgentRegistry, incident *domain.Incident, deps constrainedToolDeps) *WorkflowState {
	t.Helper()
	transition := func(_ context.Context, value *domain.Incident, status domain.IncidentStatus) error {
		value.Status = status
		return nil
	}
	deps.Transition = transition
	graph, err := buildBaselineGraph(registry, deps, transition)
	if err != nil {
		t.Fatal(err)
	}
	runnable, err := graph.Compile(context.Background(), compose.WithMaxRunSteps(GraphMaxSteps))
	if err != nil {
		t.Fatal(err)
	}
	state, err := runnable.Invoke(context.Background(), &WorkflowState{Workflow: WorkflowName, Incident: incident})
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func TestEvidenceOnlyDiagnosisBuildsObjectiveServerState(t *testing.T) {
	registry, err := NewAgentRegistry(context.Background(), scriptedEinoModel{})
	if err != nil {
		t.Fatal(err)
	}
	incident := &domain.Incident{ID: "objective-runtime", DiagnosisMethod: domain.DiagnosisMethodEvidence, Namespace: "team-a", Service: "checkout", Resource: "checkout", CreatedAt: time.Now().Add(-time.Minute)}
	deps := constrainedToolDeps{Collectors: map[string]Collector{
		"metric": cognitiveFixtureCollector{source: "prometheus"}, "log": cognitiveFixtureCollector{source: "loki"},
		"trace": cognitiveFixtureCollector{source: "jaeger"}, "topology": cognitiveFixtureCollector{source: "kubernetes"},
	}, Reasoning: reasoning.New(reasoning.DefaultConfig()), Knowledge: cognitivePatternReader{patterns: []domain.CausalPattern{incompleteCPUPattern()}}}
	state := runDeterministicBaselineGraphForTest(t, registry, incident, deps)
	if incident.Investigation == nil || len(incident.Investigation.Signals) < 4 || len(state.StateAssertions) < 3 || len(state.VerifiedHypotheses) == 0 || len(incident.Investigation.Candidates) == 0 || len(incident.Investigation.Verified) == 0 || len(incident.Investigation.Findings) != 4 {
		t.Fatalf("deterministic baseline did not produce a complete evidence chain: %+v", incident.Investigation)
	}
	if incident.Investigation.Arbitration == nil || incident.Investigation.Arbitration.Accepted {
		t.Fatalf("symptom-only candidate bypassed the deterministic causal gate: %+v", incident.Investigation.Arbitration)
	}
	for _, item := range incident.Evidence {
		if item.ID == "" || len(item.Facts) == 0 || item.Data != nil {
			t.Fatalf("evidence contract was not normalized: %+v", item)
		}
	}
}

func TestEvidenceOnlyRuntimeUsesActiveCausalPatternsForCandidatesAndCoverage(t *testing.T) {
	registry, err := NewAgentRegistry(context.Background(), scriptedEinoModel{})
	if err != nil {
		t.Fatal(err)
	}
	pattern := domain.CausalPattern{
		ID: "cpu-path", Category: "cpu", Cause: "CPU demand exceeds available capacity", Status: "active",
		Nodes: []domain.CausalNode{{ID: "cpu_demand", Type: "cause", Signals: []string{"cpu_pressure"}}, {ID: "request_failure", Type: "symptom", Signals: []string{"trace_latency", "log_error"}}},
		Edges: []domain.CausalEdge{{From: "cpu_demand", To: "request_failure"}},
	}
	incident := &domain.Incident{ID: "pattern-runtime", DiagnosisMethod: domain.DiagnosisMethodEvidence, Namespace: "team-a", Service: "checkout", Resource: "checkout", CreatedAt: time.Now().Add(-time.Minute)}
	deps := constrainedToolDeps{Collectors: map[string]Collector{
		"metric": cognitiveFixtureCollector{source: "prometheus"}, "log": cognitiveFixtureCollector{source: "loki"},
		"trace": cognitiveFixtureCollector{source: "jaeger"}, "topology": cognitiveFixtureCollector{source: "kubernetes"},
	}, Reasoning: reasoning.New(reasoning.DefaultConfig()), Knowledge: cognitivePatternReader{patterns: []domain.CausalPattern{pattern}}}
	state := runDeterministicBaselineGraphForTest(t, registry, incident, deps)
	if len(state.CausalPatterns) != 1 {
		t.Fatalf("active causal pattern was not loaded: %+v", state.CausalPatterns)
	}
	var cpu *domain.VerifiedHypothesis
	for index := range state.VerifiedHypotheses {
		if state.VerifiedHypotheses[index].Draft.Category == "cpu" {
			cpu = &state.VerifiedHypotheses[index]
			break
		}
	}
	if cpu == nil || cpu.CausalPathCoverage != 1 {
		t.Fatalf("active causal pattern was not shared by candidate and verifier: verified=%+v", state.VerifiedHypotheses)
	}
	if got := cpu.Draft.ExpectedCausalNodeIDs; !reflect.DeepEqual(got, []string{"cpu_demand", "request_failure"}) {
		t.Fatalf("candidate did not retain canonical graph path: %v", got)
	}
}

func TestRuleOnlyAndEvidenceOnlyHaveDifferentCausalFootprints(t *testing.T) {
	registry, err := NewAgentRegistry(context.Background(), scriptedEinoModel{})
	if err != nil {
		t.Fatal(err)
	}
	deps := constrainedToolDeps{Collectors: map[string]Collector{
		"metric": cognitiveFixtureCollector{source: "prometheus"}, "log": cognitiveFixtureCollector{source: "loki"},
		"trace": cognitiveFixtureCollector{source: "jaeger"}, "topology": cognitiveFixtureCollector{source: "kubernetes"},
	}, Reasoning: reasoning.New(reasoning.DefaultConfig()), Knowledge: cognitivePatternReader{patterns: []domain.CausalPattern{incompleteCPUPattern()}}}
	newIncident := func(id, method string) *domain.Incident {
		return &domain.Incident{ID: id, DiagnosisMethod: method, Namespace: "team-a", Service: "checkout", Resource: "checkout", CreatedAt: time.Now().Add(-time.Minute)}
	}
	rule := runDeterministicBaselineGraphForTest(t, registry, newIncident("rule", domain.DiagnosisMethodRuleOnly), deps)
	evidence := runDeterministicBaselineGraphForTest(t, registry, newIncident("evidence", domain.DiagnosisMethodEvidence), deps)
	if rule.Incident.Investigation.Architecture != "eino-rule-diagnosis-runtime" || len(rule.Incident.Investigation.Falsification) != 0 || len(rule.Incident.Investigation.Pairwise) != 0 {
		t.Fatalf("rule-only executed deterministic causal/falsification services: %+v", rule.Incident.Investigation)
	}
	if evidence.Incident.Investigation.Architecture != "eino-evidence-diagnosis-runtime" || len(evidence.Incident.Investigation.Falsification) == 0 {
		t.Fatalf("evidence-only did not execute its deterministic causal/falsification services: %+v", evidence.Incident.Investigation)
	}
}

func incompleteCPUPattern() domain.CausalPattern {
	return domain.CausalPattern{
		ID: "incomplete-cpu-path", Category: "cpu", Cause: "CPU demand exceeds available capacity", Status: "active",
		Nodes: []domain.CausalNode{
			{ID: "cpu_demand", Type: "cause", Signals: []string{"cpu_pressure"}},
			{ID: "unobserved_effect", Type: "symptom", Signals: []string{"unobserved_effect"}},
		},
		Edges: []domain.CausalEdge{{From: "cpu_demand", To: "unobserved_effect"}},
	}
}

func TestCognitivePreferenceCannotAlterObjectiveArbitration(t *testing.T) {
	evidence := []domain.Evidence{{ID: "metric", Source: "prometheus"}, {ID: "kube", Source: "kubernetes"}}
	verified := []domain.VerifiedHypothesis{
		{Draft: domain.HypothesisDraft{ID: "objective"}, ObjectiveScore: .84, FinalScore: .84, SupportingScore: .8, CausalPathCoverage: 1, ObservationCoverage: 1, Status: domain.HypothesisSupported, VerifiedEvidenceIDs: []string{"metric", "kube"}},
		{Draft: domain.HypothesisDraft{ID: "preferred"}, ObjectiveScore: .79, FinalScore: .79, SupportingScore: .8, CausalPathCoverage: 1, ObservationCoverage: 1, Status: domain.HypothesisSupported, VerifiedEvidenceIDs: []string{"metric", "kube"}},
	}
	result := arbitrateHypotheses(verified, evidence)
	beforeScore, beforeMargin, beforeSelected, beforeAccepted := result.SelectedScore, result.ScoreMargin, result.SelectedHypothesisID, result.Accepted
	applyTieBreakingPreference(&result, verified, []domain.TieBreakingPreference{{PreferredCandidateID: "preferred", OtherCandidateID: "objective", AssertionIDs: []string{"assertion-1"}, Predicates: []string{"co_occurs"}}})
	if result.SelectedScore != beforeScore || result.ScoreMargin != beforeMargin || result.SelectedHypothesisID != beforeSelected || result.Accepted != beforeAccepted {
		t.Fatalf("cognitive preference mutated objective arbitration: %+v", result)
	}
	if result.DisplayHypothesisID != "preferred" {
		t.Fatalf("near-tie display preference was not retained: %+v", result)
	}
}

func TestActiveInvestigationPolicyRequiresValueAndUnobservedAssertion(t *testing.T) {
	verified := []domain.VerifiedHypothesis{{Draft: domain.HypothesisDraft{ID: "cpu", Category: "cpu"}, ObjectiveScore: .72}, {Draft: domain.HypothesisDraft{ID: "database", Category: "database"}, ObjectiveScore: .66}}
	policies := []domain.InvestigationPolicy{{CandidateIDs: []string{"cpu", "database"}, ObservationKind: "connection_pressure", RationalePredicates: []string{"contradicts"}}}
	accepted, evaluated := evaluateInvestigationPolicies(policies, verified, nil)
	if len(accepted) != 1 || accepted[0].DiagnosticValue < .05 || accepted[0].DecisionImpact == 0 {
		t.Fatalf("discriminating policy was not valued by the server: %+v", accepted)
	}
	if len(evaluated) != 1 || evaluated[0].Status != "accepted" {
		t.Fatalf("accepted policy was not retained for audit: %+v", evaluated)
	}
	alreadyObserved := []domain.StateAssertion{{Property: "connection_pressure", State: "abnormal", Status: domain.StateAssertionActive}}
	if got := valueQualifiedPolicies(policies, verified, alreadyObserved); len(got) != 0 {
		t.Fatalf("already observed assertion was queried again: %+v", got)
	}
	irrelevant := []domain.InvestigationPolicy{{CandidateIDs: []string{"cpu", "database"}, ObservationKind: "network_connectivity"}}
	if got := valueQualifiedPolicies(irrelevant, verified, nil); len(got) != 0 {
		t.Fatalf("policy without a required candidate observation was accepted: %+v", got)
	}
	unknownCandidate := []domain.InvestigationPolicy{{CandidateIDs: []string{"cpu", "unknown"}, ObservationKind: "request_latency"}}
	if got := valueQualifiedPolicies(unknownCandidate, verified, nil); len(got) != 0 {
		t.Fatalf("policy containing an unknown candidate was accepted: %+v", got)
	}
}

func TestCognitiveInterpretationCompilesToBoundedServerPolicy(t *testing.T) {
	assertions := []domain.StateAssertion{{ID: "cpu", Property: "cpu_pressure", State: "abnormal", Status: domain.StateAssertionActive}}
	drafts := []domain.HypothesisDraft{{ID: "cpu", Category: "cpu"}, {ID: "database", Category: "database"}}
	verified := []domain.VerifiedHypothesis{{Draft: drafts[0], ObjectiveScore: .72}, {Draft: drafts[1], ObjectiveScore: .66}}
	record, _ := validateCognitiveResponse(1, cognitiveResponse{Interpretations: []domain.CognitiveInterpretation{{
		CandidateIDs:           []string{"cpu"},
		SupportingAssertionIDs: []string{"cpu"},
		ReasoningPredicates:    []string{"co_occurs"},
		RequiredObservations:   []string{"connection_pressure", "thread_dump"},
	}}}, assertions, drafts, verified, false)
	if len(record.InvestigationPolicies) != 1 {
		t.Fatalf("single-candidate interpretation did not compile to one server policy: %+v", record)
	}
	policy := record.InvestigationPolicies[0]
	if !reflect.DeepEqual(policy.CandidateIDs, []string{"cpu", "database"}) || policy.ObservationKind != "connection_pressure" {
		t.Fatalf("compiled policy escaped the allowed candidate/collector contract: %+v", policy)
	}
	accepted, evaluated := evaluateInvestigationPolicies(record.InvestigationPolicies, verified, assertions)
	if len(accepted) != 1 || len(evaluated) != 1 || evaluated[0].Status != "accepted" {
		t.Fatalf("compiled policy was not independently value-gated: accepted=%+v evaluated=%+v", accepted, evaluated)
	}
}

func TestPolicyOutcomeRequiresNewEvidenceAndDecisionChange(t *testing.T) {
	runtime := &CognitiveDiagnosisState{
		Verified:       []domain.VerifiedHypothesis{{Draft: domain.HypothesisDraft{ID: "cpu"}, ObjectiveScore: .78}, {Draft: domain.HypothesisDraft{ID: "database"}, ObjectiveScore: .62}},
		Investigation:  &domain.Investigation{CognitiveReasoning: []domain.CognitiveReasoning{{Round: 1, InvestigationPolicies: []domain.InvestigationPolicy{{CandidateIDs: []string{"cpu", "database"}, ObservationKind: "connection_pressure", Status: "accepted"}}}}},
		PolicyBaseline: &policyDecisionSnapshot{TopID: "database", Margin: .01, Accepted: false, Entropy: .49, NewEvidence: true},
	}
	resolvePendingPolicyOutcomes(runtime, true, domain.ArbitrationResult{SelectedHypothesisID: "cpu", ScoreMargin: .16, Accepted: true})
	if got := runtime.Investigation.CognitiveReasoning[0].InvestigationPolicies[0].Status; got != "useful" {
		t.Fatalf("new evidence that changed a decision was not marked useful: %s", got)
	}
}

func TestCausalEngineHasNoModelDependency(t *testing.T) {
	_, current, _, _ := runtime.Caller(0)
	path := filepath.Join(filepath.Dir(current), "..", "reasoning", "engine.go")
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
	if err != nil {
		t.Fatal(err)
	}
	for _, imported := range file.Imports {
		value := strings.Trim(imported.Path.Value, `"`)
		if strings.Contains(value, "cloudwego/eino") || strings.Contains(value, "/agent") || strings.Contains(value, "components/model") {
			t.Fatalf("deterministic causal engine imports cognitive/runtime dependency: %s", value)
		}
	}
	if _, err = os.Stat(path); err != nil {
		t.Fatal(err)
	}
}

func TestCognitiveResponseRejectsUngroundedPreferenceAndExpansion(t *testing.T) {
	assertions := []domain.StateAssertion{{ID: "a", Property: "cpu_pressure"}}
	drafts := []domain.HypothesisDraft{{ID: "cpu"}, {ID: "database"}}
	verified := []domain.VerifiedHypothesis{{Draft: drafts[0], ObjectiveScore: .7}, {Draft: drafts[1], ObjectiveScore: .68}}
	record, expansions := validateCognitiveResponse(1, cognitiveResponse{
		TieBreakingPreferences: []domain.TieBreakingPreference{{PreferredCandidateID: "cpu", OtherCandidateID: "database", AssertionIDs: []string{"missing"}, Predicates: []string{"invented"}}},
		ExpansionRequests:      []domain.CandidateExpansionRequest{{AssertionIDs: []string{"missing"}, RequiredObservations: []string{"cpu_pressure"}}},
	}, assertions, drafts, verified, false)
	if len(record.TieBreakingPreferences) != 0 || len(expansions) != 0 || len(record.RejectedReasons) == 0 {
		t.Fatalf("ungrounded cognitive proposal was accepted: %+v expansions=%+v", record, expansions)
	}
}

func TestUnresolvedMechanismCannotAuthorizeRecovery(t *testing.T) {
	incident := &domain.Incident{
		ID:        "unresolved-mechanism",
		Namespace: "team-a",
		Service:   "checkout",
		Resource:  "checkout",
		Evidence: []domain.Evidence{
			{ID: "metric", Source: "prometheus"},
			{ID: "kube", Source: "kubernetes"},
		},
		AgentBudget: &domain.AgentBudgetState{},
	}
	state := &WorkflowState{
		Incident: incident,
		VerifiedHypotheses: []domain.VerifiedHypothesis{{
			Draft: domain.HypothesisDraft{
				ID:       "unresolved-mechanism",
				Category: "unknown",
			},
			Status:              domain.HypothesisSupported,
			FinalScore:          .95,
			VerifiedEvidenceIDs: []string{"metric", "kube"},
		}},
	}
	runtime := &constrainedRuntime{
		state: state,
		budgets: safety.NewBudgetController(incident.AgentBudget, map[string]domain.AgentBudget{
			DiagnosisAgentName: {MaxToolUses: 2, MaxTokens: 8192, MaxCorrections: 2},
		}, nil),
		done: map[string]bool{},
	}
	output, err := submitConstrainedDiagnosis(withConstrainedRuntime(context.Background(), runtime), hypothesisSelection{HypothesisID: "unresolved-mechanism"})
	if err != nil {
		t.Fatal(err)
	}
	if output.OK || output.Feedback == nil || !strings.Contains(strings.Join(output.Feedback.MissingRequirements, " "), "requires human review") {
		t.Fatalf("unresolved mechanism was permitted to authorize recovery: %+v", output)
	}
	if state.Incident.RootCause != "" || state.DiagnosisLedger.SelectedHypothesisID != "" {
		t.Fatalf("unresolved mechanism mutated a recovery-eligible diagnosis: %+v", state)
	}
}
