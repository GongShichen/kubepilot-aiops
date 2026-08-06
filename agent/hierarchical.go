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

	"github.com/cloudwego/eino/schema"
	"github.com/kubepilot-aiops/kubepilot/internal/domain"
	"github.com/kubepilot-aiops/kubepilot/internal/safety"
	"github.com/kubepilot-aiops/kubepilot/reasoning"
)

type plannerResponse struct {
	Objective      string              `json:"objective"`
	Tasks          []domain.WorkerTask `json:"tasks"`
	StopConditions []string            `json:"stop_conditions"`
	RoundLimit     int                 `json:"round_limit"`
}

type workerResponse struct {
	Summary                    string   `json:"summary"`
	EvidenceIDs                []string `json:"evidence_ids"`
	SupportingHypothesisIDs    []string `json:"supporting_hypothesis_ids"`
	ContradictingHypothesisIDs []string `json:"contradicting_hypothesis_ids"`
	Unknowns                   []string `json:"unknowns"`
}

type argumentResponse struct {
	Hypotheses  []domain.HypothesisDraft `json:"hypotheses"`
	EvidenceIDs []string                 `json:"evidence_ids"`
	Uncertainty string                   `json:"uncertainty"`
}

type critiqueResponse struct {
	Critiques []domain.Critique `json:"critiques"`
}

func (r *AgentRegistry) runHierarchicalDiagnosis(ctx context.Context, incident *domain.Incident, deps constrainedToolDeps) (DiagnosisResult, error) {
	started := time.Now().UTC()
	investigation := &domain.Investigation{Architecture: "hierarchical-causal-react", StartedAt: started}
	causalMode, validCausalMode := domain.NormalizeCausalMode(incident.CausalMode)
	if !validCausalMode {
		return DiagnosisResult{}, fmt.Errorf("unsupported causal mode %q", incident.CausalMode)
	}
	incident.CausalMode = causalMode
	budgets := safety.NewBudgetController(incident.AgentBudget, r.limits, r.toolCosts)
	plan, usage, err := r.createInvestigationPlan(ctx, incident)
	if budgetErr := chargeAgentUsage(budgets, usage); budgetErr != nil {
		incident.AgentBudget = budgets.State()
		return DiagnosisResult{}, budgetErr
	}
	if err != nil {
		return DiagnosisResult{}, err
	}
	investigation.Plan = plan
	investigation.ModelUsage = append(investigation.ModelUsage, usage)

	var allEvidence []domain.Evidence
	var candidates []domain.RetrievalCandidate
	var activePatterns []domain.CausalPattern
	var finalHypotheses []domain.HypothesisDraft
	var selectedID string
	var finalArbitration domain.ArbitrationResult
	requestedSources := planSources(plan)

	for round := 1; round <= plan.RoundLimit; round++ {
		findings, evidence, usages, infrastructure := r.runEvidenceWorkers(ctx, incident, plan, requestedSources, deps.Collectors, budgets)
		investigation.Findings = append(investigation.Findings, findings...)
		investigation.ModelUsage = append(investigation.ModelUsage, usages...)
		for _, workerUsage := range usages {
			if err = chargeAgentUsage(budgets, workerUsage); err != nil {
				incident.AgentBudget = budgets.State()
				return DiagnosisResult{}, err
			}
		}
		allEvidence = mergeEvidence(allEvidence, evidence)
		if incident.DiagnosisLedger == nil {
			incident.DiagnosisLedger = &domain.DiagnosisLedger{}
		}
		incident.DiagnosisLedger.InfrastructureErrors = append(incident.DiagnosisLedger.InfrastructureErrors, infrastructure...)
		ranked, rankErr := deps.Reasoning.RankEvidence(incident, mergeEvidence(incident.Evidence, allEvidence))
		if rankErr != nil {
			return DiagnosisResult{}, rankErr
		}
		allEvidence = ranked.Evidence
		features := deps.Reasoning.BuildFeatures(incident, allEvidence)
		if round == 1 {
			candidates, investigation.MemoryReads = r.readIncidentMemory(ctx, incident, features, deps)
			if deps.Knowledge != nil {
				known, loadErr := deps.Knowledge.ListCausalPatterns(ctx, "active")
				if loadErr == nil {
					known = causalPatternsForMode(causalPatternsForScope(known, incident.Cluster, incident.Namespace, 0), causalMode)
					activePatterns = deps.Reasoning.MatchCausalPatterns(features, allEvidence, known)
				}
			}
		}

		primary, primaryUsage, primaryErr := r.generateArgument(ctx, DiagnosisAgentName, incident, findings, allEvidence, candidates)
		alternative, alternativeUsage, alternativeErr := r.generateArgument(ctx, AlternativeAgentName, incident, findings, allEvidence, candidates)
		if err = chargeAgentUsage(budgets, primaryUsage); err != nil {
			incident.AgentBudget = budgets.State()
			return DiagnosisResult{}, err
		}
		if err = chargeAgentUsage(budgets, alternativeUsage); err != nil {
			incident.AgentBudget = budgets.State()
			return DiagnosisResult{}, err
		}
		if primaryErr != nil {
			return DiagnosisResult{}, primaryErr
		}
		if alternativeErr != nil {
			return DiagnosisResult{}, alternativeErr
		}
		investigation.ModelUsage = append(investigation.ModelUsage, primaryUsage, alternativeUsage)
		alternative.Hypotheses = disambiguateAlternativeIDs(primary.Hypotheses, alternative.Hypotheses)
		critique, criticUsage, criticErr := r.generateCritique(ctx, incident, primary, alternative, allEvidence)
		if err = chargeAgentUsage(budgets, criticUsage); err != nil {
			incident.AgentBudget = budgets.State()
			return DiagnosisResult{}, err
		}
		if criticErr != nil {
			return DiagnosisResult{}, criticErr
		}
		investigation.ModelUsage = append(investigation.ModelUsage, criticUsage)
		investigation.Debate = append(investigation.Debate, domain.DebateRound{Round: round, Primary: primary, Alternative: alternative, Critiques: critique, OccurredAt: time.Now().UTC()})

		// Model arguments are retained in the debate record above, but only
		// server-grounded drafts may enter verification or a recovery decision.
		// A malformed proposal is an evidence gap, not a workflow failure: the
		// arbiter can request another bounded evidence round or safely leave the
		// incident unresolved.
		finalHypotheses = filterGroundedHypothesisDrafts(mergeHypothesisDrafts(primary.Hypotheses, alternative.Hypotheses), allEvidence)
		verified, verifyErr := deps.Reasoning.VerifyHypotheses(finalHypotheses, allEvidence, candidates, activePatterns)
		if verifyErr != nil {
			return DiagnosisResult{}, verifyErr
		}
		finalArbitration = arbitrateHypotheses(verified, allEvidence)
		if finalArbitration.Accepted {
			selectedID = finalArbitration.SelectedHypothesisID
			break
		}
		requestedSources = critiqueSources(critique)
		if len(requestedSources) == 0 {
			requestedSources = planSources(plan)
		}
	}

	investigation.Arbitration = &finalArbitration
	investigation.CompletedAt = time.Now().UTC()
	incident.AgentBudget = budgets.State()
	return DiagnosisResult{
		Method: domain.DiagnosisMethodKubePilot, Hypotheses: finalHypotheses,
		SelectedHypothesisID: selectedID, Evidence: allEvidence, Candidates: candidates,
		CausalPatterns: activePatterns, Investigation: investigation, BudgetAccounted: true,
	}, nil
}

func causalPatternsForMode(patterns []domain.CausalPattern, mode string) []domain.CausalPattern {
	out := make([]domain.CausalPattern, 0, len(patterns))
	for _, pattern := range patterns {
		builtIn := pattern.Source == "builtin" || pattern.Source == "static"
		switch mode {
		case domain.CausalModeNone:
			continue
		case domain.CausalModeStatic:
			if !builtIn {
				continue
			}
		case domain.CausalModeLearned:
			if builtIn {
				continue
			}
		}
		out = append(out, pattern)
	}
	return out
}

func (r *AgentRegistry) createInvestigationPlan(ctx context.Context, incident *domain.Incident) (domain.InvestigationPlan, domain.ModelUsageEvent, error) {
	payload, _ := json.Marshal(map[string]any{
		"incident":        safeIncident(incident),
		"allowed_sources": []string{"metric", "log", "trace", "topology"},
		"constraints":     map[string]any{"maximum_tasks": 4, "round_limit": 2, "topology_required": true},
	})
	started := time.Now()
	message, err := r.generateRole(ctx, PlannerAgentName, `Create the bounded investigation plan. Return exactly one JSON object with this shape and no wrapper: {"objective":"...","tasks":[{"id":"...","source":"metric|log|trace|topology","question":"...","hypothesis_ids":[],"required":true}],"stop_conditions":["..."],"round_limit":2}.`, string(payload))
	usage := r.modelUsage(incident.ID, PlannerAgentName, message, time.Since(started))
	if err != nil {
		return domain.InvestigationPlan{}, usage, err
	}
	var response plannerResponse
	if err = decodePlannerResponse(message.Content, &response); err != nil {
		return domain.InvestigationPlan{}, usage, structuredOutputError(PlannerAgentName, message, err)
	}
	plan, err := validateInvestigationPlan(response)
	return plan, usage, err
}

func decodePlannerResponse(raw string, output *plannerResponse) error {
	object, err := modelJSONObject(raw)
	if err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err = json.Unmarshal([]byte(object), &fields); err != nil {
		return err
	}
	for _, wrapper := range []string{"plan", "investigation_plan", "planner"} {
		if nested := fields[wrapper]; len(nested) > 0 {
			var candidate map[string]json.RawMessage
			if json.Unmarshal(nested, &candidate) == nil {
				fields = candidate
				break
			}
		}
	}
	if len(fields["tasks"]) == 0 {
		for _, alias := range []string{"worker_tasks", "steps"} {
			if value := fields[alias]; len(value) > 0 {
				fields["tasks"] = value
				delete(fields, alias)
				break
			}
		}
	}
	if len(fields["round_limit"]) == 0 {
		fields["round_limit"] = json.RawMessage("2")
	}
	for _, ignored := range []string{"round", "reasoning", "rationale", "analysis"} {
		delete(fields, ignored)
	}
	normalized, err := json.Marshal(fields)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(strings.NewReader(string(normalized)))
	decoder.DisallowUnknownFields()
	return decoder.Decode(output)
}

func validateInvestigationPlan(response plannerResponse) (domain.InvestigationPlan, error) {
	allowed := map[string]bool{"metric": true, "log": true, "trace": true, "topology": true, "kubernetes": true}
	seen := map[string]bool{}
	hasTopology, hasSignal := false, false
	tasks := make([]domain.WorkerTask, 0, len(response.Tasks))
	for _, task := range response.Tasks {
		source := strings.ToLower(strings.TrimSpace(task.Source))
		if !allowed[source] || seen[source] {
			continue
		}
		if source == "kubernetes" {
			source = "topology"
		}
		seen[source] = true
		hasTopology = hasTopology || source == "topology"
		hasSignal = hasSignal || source == "metric" || source == "log" || source == "trace"
		task.Source = source
		if task.ID == "" {
			task.ID = "collect-" + source
		}
		if task.Question == "" {
			task.Question = "collect evidence that supports or contradicts the active incident hypotheses"
		}
		task.Required = task.Required || source == "topology"
		tasks = append(tasks, task)
		if len(tasks) == 4 {
			break
		}
	}
	if !hasTopology || !hasSignal {
		return domain.InvestigationPlan{}, fmt.Errorf("planner must request topology and at least one operational signal source")
	}
	objective := strings.TrimSpace(response.Objective)
	if objective == "" {
		objective = "identify an evidence-grounded and falsifiable root cause"
	}
	return domain.InvestigationPlan{Objective: objective, Tasks: tasks, StopConditions: response.StopConditions, RoundLimit: 2, CreatedAt: time.Now().UTC()}, nil
}

func (r *AgentRegistry) runEvidenceWorkers(ctx context.Context, incident *domain.Incident, plan domain.InvestigationPlan, sources []string, collectors map[string]Collector, budgets *safety.BudgetController) ([]domain.WorkerFinding, []domain.Evidence, []domain.ModelUsageEvent, []string) {
	requested := map[string]bool{}
	for _, source := range sources {
		requested[source] = true
	}
	type workerResult struct {
		finding  domain.WorkerFinding
		evidence []domain.Evidence
		usage    domain.ModelUsageEvent
		err      error
	}
	results := make(chan workerResult, len(plan.Tasks))
	var group sync.WaitGroup
	for _, task := range plan.Tasks {
		if !requested[task.Source] {
			continue
		}
		task := task
		if budgets != nil {
			toolName := map[string]string{"metric": "query_prometheus_evidence", "log": "query_loki_evidence", "trace": "query_trace_evidence", "topology": "query_kubernetes_evidence"}[task.Source]
			if _, err := budgets.ReserveTool(workerName(task.Source), toolName); err != nil {
				results <- workerResult{err: err}
				continue
			}
		}
		group.Add(1)
		go func() {
			defer group.Done()
			collectorSource := task.Source
			if collectorSource == "topology" {
				collectorSource = "kubernetes"
			}
			collector := collectors[collectorSource]
			if collector == nil {
				results <- workerResult{err: fmt.Errorf("%s collector unavailable", task.Source)}
				return
			}
			items, err := collector.Collect(ctx, incident)
			if err != nil {
				results <- workerResult{err: fmt.Errorf("%s evidence unavailable: %w", task.Source, err)}
				return
			}
			finding, usage, err := r.summarizeWorkerEvidence(ctx, incident, task, items)
			results <- workerResult{finding: finding, evidence: items, usage: usage, err: err}
		}()
	}
	group.Wait()
	close(results)
	var findings []domain.WorkerFinding
	var evidence []domain.Evidence
	var usages []domain.ModelUsageEvent
	var infrastructure []string
	for result := range results {
		evidence = append(evidence, result.evidence...)
		if result.usage.Agent != "" {
			usages = append(usages, result.usage)
		}
		if result.err != nil {
			infrastructure = append(infrastructure, result.err.Error())
			continue
		}
		findings = append(findings, result.finding)
	}
	sort.SliceStable(findings, func(i, j int) bool { return findings[i].TaskID < findings[j].TaskID })
	sort.SliceStable(usages, func(i, j int) bool { return usages[i].Agent < usages[j].Agent })
	sort.Strings(infrastructure)
	return findings, evidence, usages, infrastructure
}

func chargeAgentUsage(budgets *safety.BudgetController, usage domain.ModelUsageEvent) error {
	if budgets == nil {
		return nil
	}
	agentName := strings.TrimSpace(usage.Agent)
	if agentName == "" {
		return fmt.Errorf("model usage is missing an agent identity")
	}
	if err := budgets.AddIteration(agentName); err != nil {
		return err
	}
	return budgets.AddTokens(agentName, usage.OutputTokens)
}

func (r *AgentRegistry) summarizeWorkerEvidence(ctx context.Context, incident *domain.Incident, task domain.WorkerTask, evidence []domain.Evidence) (domain.WorkerFinding, domain.ModelUsageEvent, error) {
	agentName := workerName(task.Source)
	payload, _ := json.Marshal(map[string]any{"task": task, "incident": safeIncident(incident), "evidence": compactToolEvidence(evidence, 24<<10)})
	started := time.Now()
	message, err := r.generateRole(ctx, agentName, `Summarize the supplied evidence. Return exactly one JSON object with this shape and no wrapper: {"summary":"...","evidence_ids":["supplied-id"],"supporting_hypothesis_ids":[],"contradicting_hypothesis_ids":[],"unknowns":["..."]}.`, string(payload))
	usage := r.modelUsage(incident.ID, agentName, message, time.Since(started))
	if err != nil {
		return domain.WorkerFinding{}, usage, err
	}
	var response workerResponse
	if err = decodeModelJSON(message.Content, &response); err != nil {
		return domain.WorkerFinding{}, usage, structuredOutputError(agentName, message, err)
	}
	allowed := map[string]bool{}
	for _, item := range evidence {
		allowed[item.ID] = true
	}
	ids := filterEvidenceIDs(response.EvidenceIDs, allowed)
	return domain.WorkerFinding{TaskID: task.ID, Worker: agentName, Source: task.Source, Summary: response.Summary, EvidenceIDs: ids, SupportingHypothesisIDs: response.SupportingHypothesisIDs, ContradictingHypothesisIDs: response.ContradictingHypothesisIDs, Unknowns: response.Unknowns, CompletedAt: time.Now().UTC()}, usage, nil
}

func (r *AgentRegistry) generateArgument(ctx context.Context, agentName string, incident *domain.Incident, findings []domain.WorkerFinding, evidence []domain.Evidence, candidates []domain.RetrievalCandidate) (domain.HypothesisArgument, domain.ModelUsageEvent, error) {
	payload := map[string]any{"incident": safeIncident(incident), "worker_findings": findings, "evidence": compactToolEvidence(evidence, 36<<10), "episodic_memory": compactToolCandidates(candidates, 5), "maximum_hypotheses": 3}
	raw, _ := json.Marshal(payload)
	started := time.Now()
	message, err := r.generateRole(ctx, agentName, `Return exactly one JSON object with no wrapper: {"hypotheses":[{"id":"...","category":"cpu|memory|database|network|deployment|dependency","variant":"...","cause":"...","service":"...","resource":"...","prior_probability":0.0,"supporting_evidence_ids":["supplied-id"],"contradicting_evidence_ids":[],"expected_causal_path":["cause","mechanism","symptom"],"falsification_conditions":["..."]}],"evidence_ids":["supplied-id"],"uncertainty":"..."}. Return at most three falsifiable hypotheses and cite only supplied evidence IDs.`, string(raw))
	usage := r.modelUsage(incident.ID, agentName, message, time.Since(started))
	if err != nil {
		return domain.HypothesisArgument{}, usage, err
	}
	var response argumentResponse
	if err = decodeModelJSON(message.Content, &response); err != nil {
		return domain.HypothesisArgument{}, usage, structuredOutputError(agentName, message, err)
	}
	if len(response.Hypotheses) == 0 || len(response.Hypotheses) > 3 {
		return domain.HypothesisArgument{}, usage, fmt.Errorf("%s returned an invalid hypothesis count", agentName)
	}
	return domain.HypothesisArgument{Author: agentName, Hypotheses: response.Hypotheses, EvidenceIDs: response.EvidenceIDs, Uncertainty: response.Uncertainty}, usage, nil
}

func (r *AgentRegistry) generateCritique(ctx context.Context, incident *domain.Incident, primary, alternative domain.HypothesisArgument, evidence []domain.Evidence) ([]domain.Critique, domain.ModelUsageEvent, error) {
	payload, _ := json.Marshal(map[string]any{"incident": safeIncident(incident), "primary": primary, "alternative": alternative, "evidence": compactToolEvidence(evidence, 32<<10)})
	started := time.Now()
	message, err := r.generateRole(ctx, CriticAgentName, `Challenge the competing hypotheses. Return exactly one JSON object with this shape and no wrapper: {"critiques":[{"hypothesis_id":"...","challenge":"...","missing_evidence":["..."],"contradicting_evidence_ids":["supplied-id"],"recommended_sources":["metric|log|trace|topology"]}]}.`, string(payload))
	usage := r.modelUsage(incident.ID, CriticAgentName, message, time.Since(started))
	if err != nil {
		return nil, usage, err
	}
	var response critiqueResponse
	if err = decodeModelJSON(message.Content, &response); err != nil {
		return nil, usage, structuredOutputError(CriticAgentName, message, err)
	}
	return response.Critiques, usage, nil
}

func (r *AgentRegistry) generateRole(ctx context.Context, agentName, instruction, payload string) (*schema.Message, error) {
	skill, ok := r.skills[agentName]
	if !ok {
		return nil, fmt.Errorf("skill for %s is not registered", agentName)
	}
	system := skill.Content + "\n\nRuntime instruction: " + instruction + "\nKeep the complete generated response concise and finish the required JSON well within the configured output-token limit."
	return r.chat.Generate(ctx, []*schema.Message{schema.SystemMessage(system), schema.UserMessage(payload)}, r.modelOptions()...)
}

func structuredOutputError(agentName string, message *schema.Message, err error) error {
	if message == nil {
		return fmt.Errorf("%s structured output: %w", agentName, err)
	}
	// Report lengths only. Reasoning content is neither parsed nor persisted as
	// structured output, keeping hidden reasoning outside the audit record.
	return fmt.Errorf("%s structured output (final_bytes=%d reasoning_bytes=%d): %w", agentName, len(message.Content), len(message.ReasoningContent), err)
}

func (r *AgentRegistry) readIncidentMemory(ctx context.Context, incident *domain.Incident, features domain.IncidentFeatures, deps constrainedToolDeps) ([]domain.RetrievalCandidate, []domain.MemoryAccessEvent) {
	var candidates []domain.RetrievalCandidate
	if deps.Historical != nil {
		semantic, lexical := retrieveEpisodicCandidates(ctx, deps.Historical, features)
		candidates = deps.Reasoning.Fuse(reasoning.CandidateLists{Semantic: semantic, Lexical: lexical})
		candidates = deps.Reasoning.Rerank(features, candidates)
	}
	if len(candidates) > 5 {
		candidates = candidates[:5]
	}
	terms := append([]string{incident.Service, incident.Resource}, features.Terms...)
	var events []domain.MemoryAccessEvent
	for _, kind := range []domain.MemoryKind{domain.MemoryEpisodic, domain.MemorySemantic, domain.MemoryProcedural} {
		query := domain.MemoryQuery{IncidentID: incident.ID, Agent: PlannerAgentName, Kind: kind, Scope: domain.MemoryScope{Cluster: incident.Cluster, Namespace: incident.Namespace}, Terms: terms, Limit: 5}
		raw, _ := json.Marshal(query)
		digest := sha256.Sum256(raw)
		event := domain.MemoryAccessEvent{IncidentID: incident.ID, Agent: PlannerAgentName, Kind: kind, Scope: query.Scope, QueryHash: hex.EncodeToString(digest[:]), PolicyVersion: incident.RankingPolicyHash, CreatedAt: time.Now().UTC()}
		if kind == domain.MemoryEpisodic {
			for _, candidate := range candidates {
				event.ResultIDs = append(event.ResultIDs, candidate.IncidentID)
				event.Results = append(event.Results, domain.MemoryAccessResult{ID: candidate.IncidentID, Score: candidate.Rank.FinalScore, Version: candidate.Revision})
			}
		}
		if deps.Memory != nil {
			if results, err := deps.Memory.Read(ctx, query); err == nil {
				event.ResultIDs = event.ResultIDs[:0]
				event.Results = event.Results[:0]
				for _, result := range results {
					event.ResultIDs = append(event.ResultIDs, result.ID)
					event.Results = append(event.Results, domain.MemoryAccessResult{ID: result.ID, Score: result.Score, Version: result.Version})
				}
			}
			_ = deps.Memory.RecordAccess(ctx, event)
		}
		events = append(events, event)
	}
	return candidates, events
}

func arbitrateHypotheses(verified []domain.VerifiedHypothesis, evidence []domain.Evidence) domain.ArbitrationResult {
	ordered := append([]domain.VerifiedHypothesis(nil), verified...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].FinalScore == ordered[j].FinalScore {
			return ordered[i].Draft.ID < ordered[j].Draft.ID
		}
		return ordered[i].FinalScore > ordered[j].FinalScore
	})
	result := domain.ArbitrationResult{NeedsMoreEvidence: true, Reason: "no hypothesis satisfied deterministic acceptance gates"}
	for _, item := range ordered {
		result.RankedHypothesisIDs = append(result.RankedHypothesisIDs, item.Draft.ID)
	}
	if len(ordered) == 0 {
		return result
	}
	result.SelectedHypothesisID = ordered[0].Draft.ID
	result.SelectedScore = ordered[0].FinalScore
	if len(ordered) == 1 {
		result.ScoreMargin = ordered[0].FinalScore
	} else {
		result.ScoreMargin = ordered[0].FinalScore - ordered[1].FinalScore
	}
	ranked := rankRootCause(rootRankInput{Verified: ordered, Evidence: evidence})
	result.Accepted = ranked.Selected != nil && ranked.Selected.Draft.ID == ordered[0].Draft.ID && result.ScoreMargin >= .15
	result.NeedsMoreEvidence = !result.Accepted
	if result.Accepted {
		result.Reason = "highest-ranked hypothesis passed evidence, contradiction, confidence, and margin gates"
	}
	return result
}

func planSources(plan domain.InvestigationPlan) []string {
	out := make([]string, 0, len(plan.Tasks))
	for _, task := range plan.Tasks {
		out = append(out, task.Source)
	}
	return out
}

func critiqueSources(items []domain.Critique) []string {
	allowed := map[string]bool{"metric": true, "log": true, "trace": true, "topology": true}
	seen := map[string]bool{}
	var out []string
	for _, item := range items {
		for _, source := range item.RecommendedSources {
			source = strings.ToLower(strings.TrimSpace(source))
			if source == "kubernetes" {
				source = "topology"
			}
			if allowed[source] && !seen[source] {
				seen[source] = true
				out = append(out, source)
			}
		}
	}
	sort.Strings(out)
	return out
}

func workerName(source string) string {
	switch source {
	case "metric":
		return MetricWorkerName
	case "log":
		return LogWorkerName
	case "trace":
		return TraceWorkerName
	default:
		return TopologyWorkerName
	}
}

func filterEvidenceIDs(values []string, allowed map[string]bool) []string {
	seen := map[string]bool{}
	var out []string
	for _, value := range values {
		if allowed[value] && !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	sort.Strings(out)
	return out
}

func disambiguateAlternativeIDs(primary, alternative []domain.HypothesisDraft) []domain.HypothesisDraft {
	used := map[string]bool{}
	for _, item := range primary {
		used[item.ID] = true
	}
	out := append([]domain.HypothesisDraft(nil), alternative...)
	for index := range out {
		if used[out[index].ID] {
			out[index].ID = "alternative-" + out[index].ID
		}
		used[out[index].ID] = true
	}
	return out
}

func mergeHypothesisDrafts(groups ...[]domain.HypothesisDraft) []domain.HypothesisDraft {
	seen := map[string]bool{}
	var out []domain.HypothesisDraft
	for _, group := range groups {
		for _, item := range group {
			key := strings.ToLower(strings.Join([]string{item.Cause, item.Category, item.Variant, item.Service, item.Resource}, "|"))
			if item.ID == "" || seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, item)
			if len(out) == 3 {
				return out
			}
		}
	}
	return out
}

// filterGroundedHypothesisDrafts enforces the server-owned evidence boundary
// for untrusted model output. It deliberately does not alter the original
// arguments, which remain available in Investigation.Debate for audit.
func filterGroundedHypothesisDrafts(drafts []domain.HypothesisDraft, evidence []domain.Evidence) []domain.HypothesisDraft {
	allowed := make(map[string]struct{}, len(evidence))
	for _, item := range evidence {
		if item.ID != "" {
			allowed[item.ID] = struct{}{}
		}
	}
	out := make([]domain.HypothesisDraft, 0, len(drafts))
	for _, draft := range drafts {
		if draft.ID == "" || len(draft.SupportingEvidenceIDs) == 0 || len(draft.ExpectedCausalPath) == 0 {
			continue
		}
		if !allEvidenceReferencesKnown(draft.SupportingEvidenceIDs, allowed) || !allEvidenceReferencesKnown(draft.ContradictingEvidenceIDs, allowed) {
			continue
		}
		out = append(out, draft)
	}
	return out
}

func allEvidenceReferencesKnown(ids []string, allowed map[string]struct{}) bool {
	for _, id := range ids {
		if _, ok := allowed[id]; !ok {
			return false
		}
	}
	return true
}
