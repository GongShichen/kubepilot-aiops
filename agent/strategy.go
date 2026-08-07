package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/kubepilot-aiops/kubepilot/internal/domain"
	"github.com/kubepilot-aiops/kubepilot/internal/safety"
	"github.com/kubepilot-aiops/kubepilot/reasoning"
)

type DiagnosisInput struct {
	Incident        *domain.Incident            `json:"incident"`
	InitialEvidence []domain.Evidence           `json:"initial_evidence"`
	Candidates      []domain.RetrievalCandidate `json:"candidates,omitempty"`
	MemoryScope     domain.MemoryScope          `json:"memory_scope"`
}

type DiagnosisResult struct {
	Method               string                      `json:"method"`
	Hypotheses           []domain.HypothesisDraft    `json:"hypotheses"`
	Verified             []domain.VerifiedHypothesis `json:"verified_hypotheses,omitempty"`
	Assertions           []domain.StateAssertion     `json:"state_assertions,omitempty"`
	SelectedHypothesisID string                      `json:"selected_hypothesis_id"`
	Evidence             []domain.Evidence           `json:"evidence,omitempty"`
	Candidates           []domain.RetrievalCandidate `json:"candidates,omitempty"`
	CausalPatterns       []domain.CausalPattern      `json:"causal_patterns,omitempty"`
	Investigation        *domain.Investigation       `json:"investigation,omitempty"`
	BudgetAccounted      bool                        `json:"-"`
}

type DiagnosisStrategy interface {
	ID() domain.DiagnosisMethod
	Diagnose(context.Context, DiagnosisInput) (DiagnosisResult, error)
}

type singlePassStrategy struct {
	id       string
	registry *AgentRegistry
}

func (s singlePassStrategy) ID() domain.DiagnosisMethod { return s.id }

func (s singlePassStrategy) Diagnose(ctx context.Context, input DiagnosisInput) (DiagnosisResult, error) {
	if input.Incident == nil {
		return DiagnosisResult{}, fmt.Errorf("incident is required")
	}
	payload := map[string]any{
		"incident":             safeIncident(input.Incident),
		"evidence":             compactToolEvidence(input.InitialEvidence, 48<<10),
		"allowed_causal_nodes": allowedCausalNodes(input.InitialEvidence, nil),
		"allowed_causal_edges": allowedCausalEdges(input.InitialEvidence, nil),
		"requirements": map[string]any{
			"reasoning_type":      "hypothesis_verification",
			"maximum_hypotheses":  3,
			"evidence_references": "use only evidence IDs present in the input",
			"causal_references":   "use one expected_causal_node_id belonging to cited supporting evidence, or a directed path in allowed_causal_edges",
			"output":              "JSON only with hypotheses and selected_hypothesis_id",
		},
	}
	if s.id == domain.DiagnosisMethodRAG {
		payload["episodic_memory"] = compactToolCandidates(input.Candidates, 5)
	}
	raw, _ := json.Marshal(payload)
	started := time.Now()
	message, err := s.registry.chat.Generate(ctx, []*schema.Message{
		schema.SystemMessage("Diagnose one Kubernetes incident from the supplied, server-owned observations. Return JSON only. Each hypothesis must include expected_causal_node_ids using only allowed server node IDs. A causal sequence is one cited observation node or a directed path listed in allowed_causal_edges. Do not invent evidence IDs, causal nodes, tools, actions, or hidden observations."),
		schema.UserMessage(string(raw)),
	}, s.registry.modelOptions()...)
	if err != nil {
		return DiagnosisResult{}, err
	}
	var output struct {
		Hypotheses           []domain.HypothesisDraft `json:"hypotheses"`
		SelectedHypothesisID string                   `json:"selected_hypothesis_id"`
	}
	if err = decodeModelJSON(message.Content, &output); err != nil {
		return DiagnosisResult{}, fmt.Errorf("decode %s diagnosis: %w", s.id, err)
	}
	// A baseline receives the server-owned causal-ID contract. This prevents a
	// provider response from turning a
	// list of unrelated observations into an implicit causal path.
	output.Hypotheses = filterGroundedHypothesisDrafts(output.Hypotheses, input.InitialEvidence)
	result := DiagnosisResult{Method: s.id, Hypotheses: output.Hypotheses, SelectedHypothesisID: output.SelectedHypothesisID, Evidence: input.InitialEvidence, Candidates: input.Candidates}
	architecture := "single-pass"
	if s.id == domain.DiagnosisMethodRAG {
		architecture = "single-pass-episodic"
	}
	result.Investigation = &domain.Investigation{
		Architecture: architecture,
		Plan:         domain.InvestigationPlan{Objective: "diagnose from a fixed server-owned evidence bundle", StopConditions: []string{"one structured diagnosis response"}, RoundLimit: 1, CreatedAt: time.Now().UTC()},
		StartedAt:    started.UTC(), CompletedAt: time.Now().UTC(),
		ModelUsage: []domain.ModelUsageEvent{s.registry.modelUsage(input.Incident.ID, s.id+"_diagnosis", message, time.Since(started))},
	}
	return result, nil
}

// RunBaseline executes only the explicitly selected Direct, RAG, or ReAct
// baseline. KubePilot is accepted exclusively by the Brain graph and has no
// fallback into this runtime.
func (r *AgentRegistry) RunBaseline(ctx context.Context, state *WorkflowState, deps constrainedToolDeps) error {
	if state == nil || state.Incident == nil {
		return fmt.Errorf("workflow state and Incident are required")
	}
	method, ok := domain.NormalizeDiagnosisMethod(state.Incident.DiagnosisMethod)
	if !ok {
		return fmt.Errorf("unsupported baseline method %q", state.Incident.DiagnosisMethod)
	}
	if domain.IsKubePilotBrainMethod(method) {
		return fmt.Errorf("KubePilot method %q is executable only by the Brain graph", method)
	}
	state.Incident.DiagnosisMethod = method
	switch method {
	case domain.DiagnosisMethodReAct:
		started := time.Now().UTC()
		err := r.runConstrainedAgents(ctx, state, reactDependencies(deps))
		completed := time.Now().UTC()
		usage := []domain.ModelUsageEvent(nil)
		if state.Incident.Investigation != nil {
			usage = append(usage, state.Incident.Investigation.ModelUsage...)
		}
		state.Incident.Investigation = &domain.Investigation{
			Architecture: "single-react",
			Plan:         domain.InvestigationPlan{Objective: "diagnose through one bounded ReAct agent", StopConditions: []string{"accepted hypothesis or exhausted budget"}, RoundLimit: 1, CreatedAt: started},
			ModelUsage:   usage,
			StartedAt:    started, CompletedAt: completed,
		}
		return err
	case domain.DiagnosisMethodDirect, domain.DiagnosisMethodRAG:
		result, err := r.runSinglePass(ctx, state.Incident, deps, method)
		if err != nil {
			return err
		}
		if err = r.applyDiagnosisResult(ctx, state, deps, result); err != nil {
			return err
		}
	default:
		return fmt.Errorf("method %q is not a single-pass or ReAct baseline", method)
	}
	if state.Incident.Status == domain.StatusNeedsAttention {
		return nil
	}
	return r.runConstrainedAgents(ctx, state, deps)
}

func (r *AgentRegistry) runSinglePass(ctx context.Context, incident *domain.Incident, deps constrainedToolDeps, method string) (DiagnosisResult, error) {
	evidence, infrastructure := collectInitialEvidence(ctx, incident, deps.Collectors, nil)
	if incident.DiagnosisLedger == nil {
		incident.DiagnosisLedger = &domain.DiagnosisLedger{}
	}
	incident.DiagnosisLedger.InfrastructureErrors = append(incident.DiagnosisLedger.InfrastructureErrors, infrastructure...)
	ranked, err := deps.Reasoning.RankEvidence(incident, evidence)
	if err != nil {
		return DiagnosisResult{}, err
	}
	evidence = ranked.Evidence
	features := deps.Reasoning.BuildFeatures(incident, evidence)
	var candidates []domain.RetrievalCandidate
	if method == domain.DiagnosisMethodRAG && deps.Historical != nil {
		semantic, lexical := retrieveEpisodicCandidates(ctx, deps.Historical, features)
		candidates = deps.Reasoning.Fuse(reasoning.CandidateLists{Semantic: semantic, Lexical: lexical})
		candidates = deps.Reasoning.Rerank(features, candidates)
		if len(candidates) > 5 {
			candidates = candidates[:5]
		}
	}
	strategy := singlePassStrategy{id: method, registry: r}
	result, err := strategy.Diagnose(ctx, DiagnosisInput{Incident: incident, InitialEvidence: evidence, Candidates: candidates, MemoryScope: domain.MemoryScope{Cluster: incident.Cluster, Namespace: incident.Namespace}})
	if err != nil {
		return DiagnosisResult{}, err
	}
	if method == domain.DiagnosisMethodRAG {
		digest := sha256.Sum256([]byte(incident.ID + "|episodic|" + incident.Cluster + "|" + incident.Namespace))
		event := domain.MemoryAccessEvent{IncidentID: incident.ID, Agent: DiagnosisAgentName, Kind: domain.MemoryEpisodic, Scope: domain.MemoryScope{Cluster: incident.Cluster, Namespace: incident.Namespace}, QueryHash: hex.EncodeToString(digest[:]), PolicyVersion: incident.RankingPolicyHash, CreatedAt: time.Now().UTC()}
		for _, candidate := range candidates {
			event.ResultIDs = append(event.ResultIDs, candidate.IncidentID)
			event.Results = append(event.Results, domain.MemoryAccessResult{ID: candidate.IncidentID, Score: candidate.Rank.FinalScore, Version: candidate.Revision})
		}
		result.Investigation.MemoryReads = []domain.MemoryAccessEvent{event}
		if deps.Memory != nil {
			_ = deps.Memory.RecordAccess(ctx, event)
		}
	}
	return result, nil
}

func (r *AgentRegistry) applyDiagnosisResult(ctx context.Context, state *WorkflowState, deps constrainedToolDeps, result DiagnosisResult) error {
	if state.Incident.DiagnosisLedger != nil && state.Incident.DiagnosisLedger != &state.DiagnosisLedger {
		state.DiagnosisLedger = *state.Incident.DiagnosisLedger
	}
	state.Incident.Evidence = mergeEvidence(state.Incident.Evidence, result.Evidence)
	state.RankedEvidence = append([]domain.Evidence(nil), result.Evidence...)
	state.StateAssertions = append([]domain.StateAssertion(nil), result.Assertions...)
	state.Candidates = append([]domain.RetrievalCandidate(nil), result.Candidates...)
	state.CausalPatterns = append([]domain.CausalPattern(nil), result.CausalPatterns...)
	state.Features = deps.Reasoning.BuildFeatures(state.Incident, state.RankedEvidence)
	state.Incident.Investigation = result.Investigation
	state.Incident.DiagnosisLedger = &state.DiagnosisLedger
	budgets := safety.NewBudgetController(state.Incident.AgentBudget, r.limits, r.toolCosts)
	if result.Investigation == nil {
		result.Investigation = &domain.Investigation{Architecture: result.Method, StartedAt: time.Now().UTC(), CompletedAt: time.Now().UTC()}
		state.Incident.Investigation = result.Investigation
	}
	if !result.BudgetAccounted {
		for _, usage := range result.Investigation.ModelUsage {
			agentName := usage.Agent
			if agentName == "" {
				agentName = DiagnosisAgentName
			}
			if _, known := r.limits[agentName]; !known {
				// Single-pass baseline telemetry has a strategy-specific agent
				// label, while its execution budget remains the diagnosis role.
				agentName = DiagnosisAgentName
			}
			if err := budgets.AddIteration(agentName); err != nil {
				return err
			}
			if err := budgets.AddTokens(agentName, usage.OutputTokens); err != nil {
				return err
			}
		}
	}
	runtime := &constrainedRuntime{state: state, budgets: budgets, done: map[string]bool{}, transition: deps.Transition}
	if len(result.Verified) > 0 {
		state.HypothesisDrafts = append([]domain.HypothesisDraft(nil), result.Hypotheses...)
		state.VerifiedHypotheses = append([]domain.VerifiedHypothesis(nil), result.Verified...)
		state.DiagnosisLedger.Drafts = append([]domain.HypothesisDraft(nil), result.Hypotheses...)
		state.DiagnosisLedger.Verified = append([]domain.VerifiedHypothesis(nil), result.Verified...)
	}
	runtime.hypotheses = safety.NewHypothesisTransitionService(&state.DiagnosisLedger, state.VerifiedHypotheses)
	runtimeCtx := withConstrainedRuntime(ctx, runtime)
	if len(result.Verified) == 0 {
		if _, err := recordHypotheses(runtimeCtx, HypothesisSubmission{ReasoningType: "hypothesis_verification", Hypotheses: result.Hypotheses}); err != nil {
			return err
		}
		if _, err := verifyConstrainedHypotheses(runtimeCtx, deps); err != nil {
			return err
		}
	}
	if result.Investigation.Arbitration != nil && !result.Investigation.Arbitration.Accepted {
		state.Incident.AgentBudget = budgets.State()
		return runtime.transitionIncident(ctx, domain.StatusNeedsAttention)
	}
	selected := result.SelectedHypothesisID
	if selected == "" {
		ordered := append([]domain.VerifiedHypothesis(nil), state.VerifiedHypotheses...)
		sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].FinalScore > ordered[j].FinalScore })
		if len(ordered) > 0 {
			selected = ordered[0].Draft.ID
		}
	}
	output, err := submitConstrainedDiagnosis(runtimeCtx, hypothesisSelection{HypothesisID: selected})
	state.Incident.AgentBudget = budgets.State()
	if err != nil {
		return err
	}
	if output.Feedback != nil && output.Feedback.RequiresHuman {
		return nil
	}
	if state.Incident.Status != domain.StatusProposing {
		_ = runtime.transitionIncident(ctx, domain.StatusNeedsAttention)
	}
	return nil
}

func reactDependencies(deps constrainedToolDeps) constrainedToolDeps {
	deps.Historical = nil
	deps.Knowledge = nil
	deps.Reranker = nil
	deps.Causal = nil
	deps.TopologyPatterns = nil
	deps.CausalPatterns = nil
	deps.DiscoveredPatterns = nil
	deps.Memory = nil
	return deps
}

func collectInitialEvidence(ctx context.Context, incident *domain.Incident, collectors map[string]Collector, sources []string) ([]domain.Evidence, []string) {
	if len(sources) == 0 {
		sources = []string{"metric", "log", "trace", "kubernetes"}
	}
	type collected struct {
		source string
		items  []domain.Evidence
		err    error
	}
	results := make(chan collected, len(sources))
	var group sync.WaitGroup
	for _, source := range sources {
		collector := collectors[source]
		if collector == nil {
			results <- collected{source: source, err: fmt.Errorf("collector unavailable")}
			continue
		}
		group.Add(1)
		go func(source string, collector Collector) {
			defer group.Done()
			items, err := collector.Collect(ctx, incident, defaultEvidenceRequest(incident, source))
			results <- collected{source: source, items: items, err: err}
		}(source, collector)
	}
	group.Wait()
	close(results)
	var evidence []domain.Evidence
	var infrastructure []string
	for result := range results {
		if result.err != nil {
			infrastructure = append(infrastructure, result.source+" evidence unavailable")
			continue
		}
		evidence = append(evidence, normalizeCollectedEvidence(incident, result.items)...)
	}
	sort.SliceStable(evidence, func(i, j int) bool {
		if evidence[i].Source == evidence[j].Source {
			return evidence[i].ID < evidence[j].ID
		}
		return evidence[i].Source < evidence[j].Source
	})
	sort.Strings(infrastructure)
	return mergeEvidence(incident.Evidence, evidence), infrastructure
}

func retrieveEpisodicCandidates(ctx context.Context, historical HistoricalCandidateRetriever, features domain.IncidentFeatures) ([]domain.RetrievalCandidate, []domain.RetrievalCandidate) {
	if historical == nil {
		return nil, nil
	}
	var semantic, lexical []domain.RetrievalCandidate
	var group sync.WaitGroup
	group.Add(2)
	go func() { defer group.Done(); semantic, _ = historical.Semantic(ctx, features, 50) }()
	go func() { defer group.Done(); lexical, _ = historical.Lexical(ctx, features, 50) }()
	group.Wait()
	return semantic, lexical
}

func mergeEvidence(left, right []domain.Evidence) []domain.Evidence {
	byID := make(map[string]domain.Evidence, len(left)+len(right))
	for _, item := range append(append([]domain.Evidence(nil), left...), right...) {
		if item.ID != "" {
			byID[item.ID] = item
		}
	}
	out := make([]domain.Evidence, 0, len(byID))
	for _, item := range byID {
		out = append(out, item)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func causalPatternsForScope(items []domain.CausalPattern, cluster, namespace string, limit int) []domain.CausalPattern {
	out := make([]domain.CausalPattern, 0, len(items))
	for _, item := range items {
		if item.Cluster != "" && item.Cluster != cluster {
			continue
		}
		if item.Namespace != "" && item.Namespace != namespace {
			continue
		}
		out = append(out, item)
		if limit > 0 && len(out) == limit {
			break
		}
	}
	return out
}

func decodeModelJSON(raw string, output any) error {
	object, err := modelJSONObject(raw)
	if err != nil {
		return err
	}
	// Model providers occasionally add explanatory fields around an otherwise
	// valid structured response. The server validates every field it consumes
	// later in the constrained handoff, so ignoring unknown fields keeps the
	// protocol forward-compatible without granting those fields any authority.
	return json.Unmarshal([]byte(object), output)
}

func modelJSONObject(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	start, end := strings.Index(raw, "{"), strings.LastIndex(raw, "}")
	if start < 0 || end < start {
		return "", fmt.Errorf("model response did not contain a JSON object")
	}
	return raw[start : end+1], nil
}

func (r *AgentRegistry) modelOptions() []model.Option {
	options := []model.Option{model.WithTemperature(0)}
	if r.requestMaxTokens > 0 {
		options = append(options, model.WithMaxTokens(r.requestMaxTokens))
	}
	return options
}

func (r *AgentRegistry) modelUsage(incidentID, agent string, message *schema.Message, duration time.Duration) domain.ModelUsageEvent {
	parent, phase := SupervisorAgentName, "diagnosis"
	switch agent {
	case MetricWorkerName, LogWorkerName, TraceWorkerName, TopologyWorkerName, DiagnosisAgentName, AlternativeAgentName, CriticAgentName, CognitiveRuntimeName:
		parent = PlannerAgentName
	case RecoveryAgentName:
		phase = "recovery"
	}
	usage := domain.ModelUsageEvent{IncidentID: incidentID, Agent: agent, ParentAgent: parent, Phase: phase, DurationMS: duration.Seconds() * 1000, CreatedAt: time.Now().UTC()}
	if message != nil && message.ResponseMeta != nil && message.ResponseMeta.Usage != nil {
		usage.InputTokens = message.ResponseMeta.Usage.PromptTokens
		usage.OutputTokens = message.ResponseMeta.Usage.CompletionTokens
		if usage.OutputTokens == 0 && message.ResponseMeta.Usage.TotalTokens > usage.InputTokens {
			usage.OutputTokens = message.ResponseMeta.Usage.TotalTokens - usage.InputTokens
		}
		usage.ReasoningTokens = message.ResponseMeta.Usage.CompletionTokensDetails.ReasoningTokens
	}
	visibleOutputTokens := max(0, usage.OutputTokens-usage.ReasoningTokens)
	usage.EstimatedCost = (float64(usage.InputTokens)*r.inputPricePerMillion + float64(visibleOutputTokens)*r.outputPricePerMillion + float64(usage.ReasoningTokens)*r.reasoningPricePerMillion) / 1_000_000
	return usage
}
