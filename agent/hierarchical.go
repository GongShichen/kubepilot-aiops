package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	openmodel "github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/components/model"
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
	requests := planRequests(plan, incident)
	executedRequests := map[string]bool{}
	existingEvidenceIDs := map[string]bool{}
	for _, item := range incident.Evidence {
		existingEvidenceIDs[item.ID] = true
	}

	for round := 1; round <= plan.RoundLimit; round++ {
		uniqueRequests := make([]domain.EvidenceRequest, 0, len(requests))
		for _, request := range requests {
			fingerprint := evidenceRequestFingerprint(request)
			if executedRequests[fingerprint] {
				workerRequestDuplicate.Inc()
				continue
			}
			executedRequests[fingerprint] = true
			uniqueRequests = append(uniqueRequests, request)
		}
		if len(uniqueRequests) == 0 {
			debateWithoutNewEvidence.Inc()
			if finalArbitration.Reason == "" {
				finalArbitration = domain.ArbitrationResult{NeedsMoreEvidence: true, Reason: "supplemental evidence request duplicated the completed collection"}
			}
			break
		}
		allowedTargets := allowedEvidenceTargets(incident, allEvidence)
		findings, collected, usages, infrastructure := r.runEvidenceWorkers(ctx, incident, plan, uniqueRequests, deps.Collectors, budgets, allowedTargets, existingEvidenceIDs)
		investigation.Findings = append(investigation.Findings, findings...)
		investigation.ModelUsage = append(investigation.ModelUsage, usages...)
		for _, workerUsage := range usages {
			if err = chargeAgentUsage(budgets, workerUsage); err != nil {
				incident.AgentBudget = budgets.State()
				return DiagnosisResult{}, err
			}
		}
		if round > 1 && len(collected) == 0 {
			debateWithoutNewEvidence.Inc()
			finalArbitration.NeedsMoreEvidence = true
			finalArbitration.Accepted = false
			finalArbitration.Reason = "supplemental collection produced no new logical evidence"
			break
		}
		for _, item := range collected {
			existingEvidenceIDs[item.ID] = true
		}
		allEvidence = mergeEvidence(allEvidence, collected)
		if incident.DiagnosisLedger == nil {
			incident.DiagnosisLedger = &domain.DiagnosisLedger{}
		}
		incident.DiagnosisLedger.InfrastructureErrors = append(incident.DiagnosisLedger.InfrastructureErrors, infrastructure...)
		ranked, rankErr := deps.Reasoning.RankEvidence(incident, mergeEvidence(incident.Evidence, allEvidence))
		if rankErr != nil {
			return DiagnosisResult{}, rankErr
		}
		allEvidence = ranked.Evidence
		if round == 1 {
			features := deps.Reasoning.BuildFeatures(incident, allEvidence)
			candidates, investigation.MemoryReads = r.readIncidentMemory(ctx, incident, features, deps)
			if deps.Knowledge != nil {
				known, loadErr := deps.Knowledge.ListCausalPatterns(ctx, "active")
				if loadErr == nil {
					known = causalPatternsForMode(causalPatternsForScope(known, incident.Cluster, incident.Namespace, 0), causalMode)
					allEvidence = deps.Reasoning.AnnotateCausalNodes(allEvidence, known)
					features = deps.Reasoning.BuildFeatures(incident, allEvidence)
					activePatterns = deps.Reasoning.MatchCausalPatterns(features, allEvidence, known)
				}
			}
		}

		allEvidence = deps.Reasoning.AnnotateCausalNodes(allEvidence, activePatterns)
		primary, primaryUsage, primaryErr := r.generateArgument(ctx, DiagnosisAgentName, incident, findings, allEvidence, candidates, activePatterns)
		alternative, alternativeUsage, alternativeErr := r.generateArgument(ctx, AlternativeAgentName, incident, findings, allEvidence, candidates, activePatterns)
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
		finalHypotheses = filterGroundedHypothesisDrafts(mergeHypothesisDrafts(primary.Hypotheses, alternative.Hypotheses), allEvidence, activePatterns)
		verified, verifyErr := deps.Reasoning.VerifyHypotheses(finalHypotheses, allEvidence, candidates, activePatterns)
		if verifyErr != nil {
			return DiagnosisResult{}, verifyErr
		}
		finalArbitration = arbitrateHypotheses(verified, allEvidence)
		if finalArbitration.Accepted {
			selectedID = finalArbitration.SelectedHypothesisID
			break
		}
		requests = critiqueEvidenceRequests(incident, plan, critique, finalHypotheses, allEvidence)
		if len(requests) == 0 {
			finalArbitration.Reason = "critic identified no server-actionable evidence gap"
			break
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
	if err == nil {
		plan = attachPlanRequests(plan, incident)
	}
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

func attachPlanRequests(plan domain.InvestigationPlan, incident *domain.Incident) domain.InvestigationPlan {
	for index := range plan.Tasks {
		request := defaultEvidenceRequest(incident, plan.Tasks[index].Source)
		request.HypothesisIDs = append([]string(nil), plan.Tasks[index].HypothesisIDs...)
		plan.Tasks[index].Request = request
	}
	return plan
}

func planRequests(plan domain.InvestigationPlan, incident *domain.Incident) []domain.EvidenceRequest {
	out := make([]domain.EvidenceRequest, 0, len(plan.Tasks))
	for _, task := range plan.Tasks {
		request := task.Request
		if request.Source == "" {
			request = defaultEvidenceRequest(incident, task.Source)
		}
		request.HypothesisIDs = append([]string(nil), task.HypothesisIDs...)
		out = append(out, request)
	}
	return out
}

func (r *AgentRegistry) runEvidenceWorkers(ctx context.Context, incident *domain.Incident, plan domain.InvestigationPlan, requests []domain.EvidenceRequest, collectors map[string]Collector, budgets *safety.BudgetController, allowedTargets, existingIDs map[string]bool) ([]domain.WorkerFinding, []domain.Evidence, []domain.ModelUsageEvent, []string) {
	if len(requests) == 0 {
		requests = planRequests(plan, incident)
	}
	tasks := map[string]domain.WorkerTask{}
	for _, task := range plan.Tasks {
		tasks[canonicalWorkerSource(task.Source)] = task
	}
	type workerResult struct {
		finding  domain.WorkerFinding
		evidence []domain.Evidence
		usage    domain.ModelUsageEvent
		err      error
	}
	results := make(chan workerResult, len(requests))
	var group sync.WaitGroup
	for requestIndex, rawRequest := range requests {
		request, requestErr := validateEvidenceRequest(incident, rawRequest, rawRequest.Source, allowedTargets)
		if requestErr != nil {
			results <- workerResult{err: requestErr}
			continue
		}
		task, ok := tasks[canonicalWorkerSource(request.Source)]
		if !ok {
			results <- workerResult{err: fmt.Errorf("no worker task for evidence source %q", request.Source)}
			continue
		}
		if requestIndex > 0 || len(requests) > len(plan.Tasks) {
			task.ID = fmt.Sprintf("%s-%d", task.ID, requestIndex+1)
		}
		task.Request = request
		capturedTask := task
		capturedRequest := request
		if budgets != nil {
			toolName := map[string]string{"metric": "query_prometheus_evidence", "log": "query_loki_evidence", "trace": "query_trace_evidence", "topology": "query_kubernetes_evidence"}[capturedTask.Source]
			if _, err := budgets.ReserveTool(workerName(capturedTask.Source), toolName); err != nil {
				results <- workerResult{err: err}
				continue
			}
		}
		group.Add(1)
		go func() {
			defer group.Done()
			collectorSource := capturedTask.Source
			if collectorSource == "topology" {
				collectorSource = "kubernetes"
			}
			collector := collectors[collectorSource]
			if collector == nil {
				results <- workerResult{err: fmt.Errorf("%s collector unavailable", capturedTask.Source)}
				return
			}
			items, err := collector.Collect(ctx, incident, capturedRequest)
			if err != nil {
				results <- workerResult{err: fmt.Errorf("%s evidence unavailable: %w", capturedTask.Source, err)}
				return
			}
			if len(existingIDs) > 0 {
				fresh := items[:0]
				for _, item := range items {
					if !existingIDs[item.ID] {
						fresh = append(fresh, item)
					}
				}
				items = fresh
				if len(items) == 0 {
					results <- workerResult{}
					return
				}
			}
			finding, usage, err := r.summarizeWorkerEvidence(ctx, incident, capturedTask, items)
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
	payload, _ := json.Marshal(map[string]any{"task": task, "incident": safeIncident(incident), "evidence": compactEvidenceViews(evidence, 24<<10)})
	started := time.Now()
	message, err := r.generateRole(ctx, agentName, `Summarize the supplied evidence. Return exactly one JSON object with this shape and no wrapper: {"summary":"...","evidence_ids":["supplied-id"],"supporting_hypothesis_ids":[],"contradicting_hypothesis_ids":[],"unknowns":["..."]}. Facts and anomaly_score are server-derived observations: state explicit policy effects, endpoint state, and runtime-concurrency changes exactly as supplied. Do not call a workload healthy when facts report an isolation effect or other anomaly. A request-rate change alone does not establish external demand; distinguish it from concurrent internal runtime pressure when the evidence permits.`, string(payload))
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

func (r *AgentRegistry) generateArgument(ctx context.Context, agentName string, incident *domain.Incident, findings []domain.WorkerFinding, evidence []domain.Evidence, candidates []domain.RetrievalCandidate, patterns []domain.CausalPattern) (domain.HypothesisArgument, domain.ModelUsageEvent, error) {
	payload := map[string]any{"incident": safeIncident(incident), "worker_findings": findings, "evidence": compactEvidenceViews(evidence, 36<<10), "episodic_memory": compactToolCandidates(candidates, 5), "allowed_causal_nodes": allowedCausalNodes(evidence, patterns), "allowed_causal_edges": allowedCausalEdges(evidence, patterns), "maximum_hypotheses": 3}
	raw, _ := json.Marshal(payload)
	started := time.Now()
	message, err := r.generateRole(ctx, agentName, `Return exactly one JSON object with no wrapper: {"hypotheses":[{"id":"...","category":"cpu|memory|database|network|deployment|dependency","variant":"...","cause":"...","service":"...","resource":"...","prior_probability":0.0,"supporting_evidence_ids":["supplied-id"],"contradicting_evidence_ids":[],"expected_causal_node_ids":["supplied-node-id"],"falsification_conditions":["..."]}],"evidence_ids":["supplied-id"],"uncertainty":"..."}. Return at most three falsifiable hypotheses. The variant must be a concise stable snake_case mechanism, rather than a prose restatement of a symptom; use the same mechanism label when the current evidence supports the same explanation. Cite only supplied evidence IDs and allowed causal node IDs; never invent causal nodes. A causal sequence must be either one observation node belonging to cited supporting evidence, one observed pattern node, or a directed path listed by allowed_causal_edges. Do not append signal IDs or disconnected nodes.`, string(raw))
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
	payload, _ := json.Marshal(map[string]any{"incident": safeIncident(incident), "primary": primary, "alternative": alternative, "evidence": compactEvidenceViews(evidence, 32<<10)})
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
	messages := []*schema.Message{schema.SystemMessage(system), schema.UserMessage(payload)}
	response, err := r.chat.Generate(ctx, messages, r.structuredModelOptions()...)
	if err != nil || validStructuredResponse(response) {
		return response, err
	}
	// Some reasoning-capable providers can finish a streamed response with only
	// hidden reasoning content. Retry that protocol failure once as a fresh,
	// explicitly-visible JSON response. The retry preserves the configured
	// per-response output limit and aggregates both attempts into usage telemetry.
	retrySystem := system + "\n\nRetry requirement: the prior response did not contain a valid visible JSON object. Return the requested JSON object immediately, with no prose or hidden analysis."
	retryMessages := []*schema.Message{schema.SystemMessage(retrySystem), schema.UserMessage(payload)}
	retry, retryErr := r.chat.Generate(ctx, retryMessages, r.structuredModelOptions()...)
	if retryErr != nil {
		return response, fmt.Errorf("retry visible JSON response: %w", retryErr)
	}
	mergeResponseUsage(retry, response)
	return retry, nil
}

func validStructuredResponse(message *schema.Message) bool {
	if message == nil {
		return false
	}
	object, err := modelJSONObject(message.Content)
	return err == nil && json.Valid([]byte(object))
}

func mergeResponseUsage(final, previous *schema.Message) {
	if final == nil || previous == nil || previous.ResponseMeta == nil || previous.ResponseMeta.Usage == nil {
		return
	}
	if final.ResponseMeta == nil {
		final.ResponseMeta = &schema.ResponseMeta{}
	}
	if final.ResponseMeta.Usage == nil {
		usage := *previous.ResponseMeta.Usage
		final.ResponseMeta.Usage = &usage
		return
	}
	current := final.ResponseMeta.Usage
	prior := previous.ResponseMeta.Usage
	current.PromptTokens += prior.PromptTokens
	current.PromptTokenDetails.CachedTokens += prior.PromptTokenDetails.CachedTokens
	current.CompletionTokens += prior.CompletionTokens
	current.CompletionTokensDetails.ReasoningTokens += prior.CompletionTokensDetails.ReasoningTokens
	current.TotalTokens += prior.TotalTokens
}

func (r *AgentRegistry) structuredModelOptions() []model.Option {
	options := append([]model.Option(nil), r.modelOptions()...)
	// Hierarchical roles return server-validated protocol objects rather than
	// free-form prose. Request JSON mode only for these calls: ReAct continues
	// to use normal tool calling, and hidden reasoning is never parsed or
	// retained as a structured result.
	return append(options, openmodel.WithExtraFields(map[string]any{
		"response_format": map[string]string{"type": "json_object"},
	}))
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
		// An empty deterministic candidate universe is an auditable abstention,
		// not an absent arbitration record.  Downstream monitoring uses gate
		// results to distinguish evidence collection failures from a safe
		// "unresolved mechanism" outcome and to apply the consecutive-gate pause
		// rule consistently.
		arbitrationGateFailure.WithLabelValues("no_candidate").Inc()
		result.GateResults = []domain.HypothesisGateResult{{FailedGates: []string{"no_candidate"}}}
		return result
	}
	result.SelectedHypothesisID = ordered[0].Draft.ID
	result.SelectedScore = ordered[0].FinalScore
	if len(ordered) == 1 {
		result.ScoreMargin = ordered[0].FinalScore
	} else {
		result.ScoreMargin = ordered[0].FinalScore - ordered[1].FinalScore
	}
	for index, item := range ordered {
		failed := hypothesisGateFailures(item, evidence)
		if index == 0 && result.ScoreMargin < .15 {
			failed = append(failed, "score_margin")
		}
		for _, gate := range failed {
			arbitrationGateFailure.WithLabelValues(gate).Inc()
		}
		breakdown := domain.HypothesisConfidenceRecord{
			HypothesisID: item.Draft.ID, Score: item.FinalScore, ObjectiveScore: item.ObjectiveScore, ObservationCoverage: item.ObservationCoverage,
			ModelPrior:      item.Draft.PriorProbability,
			SupportingScore: item.SupportingScore, ContradictionScore: item.ContradictionScore,
			CausalPathCoverage: item.CausalPathCoverage, HistoricalRelevance: item.HistoricalRelevance,
			TopologyRelevance: item.TopologyRelevance, ComputedAt: time.Now().UTC(),
			EvidenceSourceCount: evidenceSourceCount(item.VerifiedEvidenceIDs, evidence),
		}
		if len(item.ConfidenceHistory) > 0 {
			breakdown = item.ConfidenceHistory[len(item.ConfidenceHistory)-1]
		}
		result.GateResults = append(result.GateResults, domain.HypothesisGateResult{HypothesisID: item.Draft.ID, ScoreBreakdown: breakdown, FailedGates: failed})
	}
	ranked := rankRootCause(rootRankInput{Verified: ordered, Evidence: evidence})
	result.Accepted = ranked.Selected != nil && ranked.Selected.Draft.ID == ordered[0].Draft.ID && result.ScoreMargin >= .15
	result.NeedsMoreEvidence = !result.Accepted
	if result.Accepted {
		result.Reason = "highest-ranked hypothesis passed evidence, contradiction, confidence, and margin gates"
	}
	return result
}

func hypothesisGateFailures(item domain.VerifiedHypothesis, evidence []domain.Evidence) []string {
	var failed []string
	if item.Status != domain.HypothesisSupported && item.Status != domain.HypothesisAccepted {
		failed = append(failed, "supported_status")
	}
	if item.SupportingScore < .65 {
		failed = append(failed, "supporting_score")
	}
	if len(item.MissingCausalNodes) > 0 || item.CausalPathCoverage < 1 {
		failed = append(failed, "causal_coverage")
	}
	if item.FinalScore < .80 {
		failed = append(failed, "final_score")
	}
	if item.ContradictionScore > .10 {
		failed = append(failed, "contradiction")
	}
	if len(item.VerifiedEvidenceIDs) < 2 {
		failed = append(failed, "evidence_count")
	}
	sources, hasKubernetes := evidenceSources(item.VerifiedEvidenceIDs, evidence)
	if len(sources) < 2 {
		failed = append(failed, "independent_sources")
	}
	if !hasKubernetes {
		failed = append(failed, "kubernetes_evidence")
	}
	return failed
}

func evidenceSources(ids []string, evidence []domain.Evidence) (map[string]bool, bool) {
	allowed := map[string]domain.Evidence{}
	for _, item := range evidence {
		allowed[item.ID] = item
	}
	sources := map[string]bool{}
	hasKubernetes := false
	for _, id := range ids {
		if item, ok := allowed[id]; ok {
			sources[item.Source] = true
			hasKubernetes = hasKubernetes || item.Source == "kubernetes"
		}
	}
	return sources, hasKubernetes
}

func evidenceSourceCount(ids []string, evidence []domain.Evidence) int {
	sources, _ := evidenceSources(ids, evidence)
	return len(sources)
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

func critiqueEvidenceRequests(incident *domain.Incident, plan domain.InvestigationPlan, critiques []domain.Critique, hypotheses []domain.HypothesisDraft, evidence []domain.Evidence) []domain.EvidenceRequest {
	sources := critiqueSources(critiques)
	if len(sources) == 0 {
		return nil
	}
	baseBySource := map[string]domain.EvidenceRequest{}
	for _, request := range planRequests(plan, incident) {
		baseBySource[canonicalWorkerSource(request.Source)] = request
	}
	allowed := allowedEvidenceTargets(incident, evidence)
	targets := []domain.ResourceRef{{Namespace: incident.Namespace, Service: incident.Service, Resource: incident.Resource}}
	for _, hypothesis := range hypotheses {
		identity := resourceIdentity(hypothesis.Service, hypothesis.Resource)
		if identity == "" || !allowed[identity] || identity == resourceIdentity(incident.Service, incident.Resource) {
			continue
		}
		targets = append(targets, domain.ResourceRef{Namespace: incident.Namespace, Service: hypothesis.Service, Resource: hypothesis.Service, Kind: "service"})
	}
	missing := make([]string, 0)
	hypothesisIDs := make([]string, 0)
	for _, critique := range critiques {
		missing = append(missing, critique.MissingEvidence...)
		if critique.HypothesisID != "" {
			hypothesisIDs = append(hypothesisIDs, critique.HypothesisID)
		}
	}
	signals := signalKindsFromMissingEvidence(missing)
	requests := make([]domain.EvidenceRequest, 0, len(sources)*len(targets))
	for _, source := range sources {
		base := baseBySource[source]
		if base.Source == "" {
			base = defaultEvidenceRequest(incident, source)
		}
		base.Source = source
		base.SignalKinds = signals
		base.HypothesisIDs = normalizeStrings(hypothesisIDs)
		for _, target := range targets {
			request := base
			request.Targets = []domain.ResourceRef{target}
			requests = append(requests, request)
		}
	}
	return requests
}

func signalKindsFromMissingEvidence(items []string) []string {
	text := strings.ToLower(strings.Join(items, " "))
	// This is a source-agnostic observation vocabulary, not a fault or case
	// classifier. It only narrows collectors to facts explicitly requested by
	// the critic.
	vocabulary := []string{"cpu", "memory", "latency", "error", "throughput", "restart", "ready", "replica", "endpoint", "probe", "resource", "config", "network", "dependency", "connection", "saturation", "trace", "log"}
	var out []string
	for _, signal := range vocabulary {
		if strings.Contains(text, signal) {
			out = append(out, signal)
		}
	}
	return out
}

func allowedEvidenceTargets(incident *domain.Incident, evidence []domain.Evidence) map[string]bool {
	allowed := map[string]bool{resourceIdentity(incident.Service, incident.Resource): true}
	for _, item := range evidence {
		facts := item.Facts
		if len(facts) == 0 {
			facts = item.Content
		}
		for _, key := range []string{"discovered_dependencies", "dependencies"} {
			for _, dependency := range stringSlice(facts[key]) {
				allowed[resourceIdentity(dependency, dependency)] = true
			}
		}
		for _, key := range []string{"slow_service", "error_service", "dependency"} {
			if value, ok := facts[key].(string); ok && value != "" {
				allowed[resourceIdentity(value, value)] = true
			}
		}
	}
	return allowed
}

func stringSlice(value any) []string {
	switch typed := value.(type) {
	case []string:
		return typed
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if value, ok := item.(string); ok {
				out = append(out, value)
			}
		}
		return out
	default:
		return nil
	}
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
			if item.ID == "" || seen[key] || containsEquivalentHypothesis(out, item) {
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

// containsEquivalentHypothesis removes duplicate wording from the blind
// alternative pass before arbitration. It requires the same target, causal
// mechanism, and overlapping evidence as well as a shared specific mechanism
// token; superficially similar resource hypotheses (for example load versus a
// configured limit) remain separate alternatives.
func containsEquivalentHypothesis(existing []domain.HypothesisDraft, candidate domain.HypothesisDraft) bool {
	for _, item := range existing {
		if !sameHypothesisTarget(item, candidate) || !sameStringSet(item.ExpectedCausalNodeIDs, candidate.ExpectedCausalNodeIDs) {
			continue
		}
		if stringSetJaccard(item.SupportingEvidenceIDs, candidate.SupportingEvidenceIDs) < .5 {
			continue
		}
		if stringSetJaccard(specificMechanismTokens(item), specificMechanismTokens(candidate)) > 0 {
			return true
		}
	}
	return false
}

func sameHypothesisTarget(left, right domain.HypothesisDraft) bool {
	return strings.EqualFold(strings.TrimSpace(left.Category), strings.TrimSpace(right.Category)) &&
		strings.EqualFold(strings.TrimSpace(left.Service), strings.TrimSpace(right.Service)) &&
		strings.EqualFold(strings.TrimSpace(left.Resource), strings.TrimSpace(right.Resource))
}

func sameStringSet(left, right []string) bool {
	if len(left) == 0 || len(left) != len(right) {
		return false
	}
	return stringSetJaccard(left, right) == 1
}

func stringSetJaccard(left, right []string) float64 {
	leftSet, rightSet := map[string]bool{}, map[string]bool{}
	for _, value := range left {
		if value = strings.ToLower(strings.TrimSpace(value)); value != "" {
			leftSet[value] = true
		}
	}
	for _, value := range right {
		if value = strings.ToLower(strings.TrimSpace(value)); value != "" {
			rightSet[value] = true
		}
	}
	if len(leftSet) == 0 || len(rightSet) == 0 {
		return 0
	}
	intersection := 0
	for value := range leftSet {
		if rightSet[value] {
			intersection++
		}
	}
	return float64(intersection) / float64(len(leftSet)+len(rightSet)-intersection)
}

func specificMechanismTokens(item domain.HypothesisDraft) []string {
	ignored := map[string]bool{
		"a": true, "an": true, "and": true, "cause": true, "caused": true,
		"cpu": true, "memory": true, "network": true, "database": true,
		"error": true, "failure": true, "high": true, "latency": true,
		"low": true, "pressure": true, "resource": true, "root": true,
		"saturation": true, "service": true, "the": true, "to": true,
	}
	values := strings.FieldsFunc(strings.ToLower(item.Cause+" "+item.Variant), func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < '0' || r > '9')
	})
	seen := map[string]bool{}
	for _, value := range values {
		if len(value) >= 3 && !ignored[value] {
			seen[value] = true
		}
	}
	out := make([]string, 0, len(seen))
	for value := range seen {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

// filterGroundedHypothesisDrafts enforces the server-owned evidence boundary
// for untrusted model output. It deliberately does not alter the original
// arguments, which remain available in Investigation.Debate for audit.
func filterGroundedHypothesisDrafts(drafts []domain.HypothesisDraft, evidence []domain.Evidence, patternSets ...[]domain.CausalPattern) []domain.HypothesisDraft {
	allowed := make(map[string]struct{}, len(evidence))
	patterns := flattenCausalPatterns(patternSets...)
	allowedNodes := causalNodeAllowlist(evidence, patterns)
	allowedEdges := causalEdgeAllowlist(evidence, patterns)
	for _, item := range evidence {
		if item.ID != "" {
			allowed[item.ID] = struct{}{}
		}
	}
	out := make([]domain.HypothesisDraft, 0, len(drafts))
	for _, draft := range drafts {
		if draft.ID == "" || len(draft.SupportingEvidenceIDs) == 0 || len(draft.ExpectedCausalNodeIDs) == 0 {
			continue
		}
		if !allEvidenceReferencesKnown(draft.SupportingEvidenceIDs, allowed) || !allEvidenceReferencesKnown(draft.ContradictingEvidenceIDs, allowed) {
			continue
		}
		if !allEvidenceReferencesKnown(draft.ExpectedCausalNodeIDs, allowedNodes) || !causalPathIsServerValid(draft.ExpectedCausalNodeIDs, draft.SupportingEvidenceIDs, allowedEdges) {
			continue
		}
		// Downstream causal learning keeps the legacy storage field for schema
		// compatibility, but it now contains canonical server node IDs only.
		draft.ExpectedCausalPath = append([]string(nil), draft.ExpectedCausalNodeIDs...)
		out = append(out, draft)
	}
	return out
}

func allowedCausalNodes(evidence []domain.Evidence, patterns []domain.CausalPattern) []map[string]string {
	values := causalNodeAllowlist(evidence, patterns)
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]map[string]string, 0, len(keys))
	for _, key := range keys {
		value := map[string]string{"id": key, "type": "observed_pattern_node"}
		if strings.HasPrefix(key, "obs:") {
			value["type"] = "observation"
			value["evidence_id"] = strings.TrimPrefix(key, "obs:")
		}
		out = append(out, value)
	}
	return out
}

func allowedCausalEdges(evidence []domain.Evidence, patterns []domain.CausalPattern) []map[string]string {
	edges := causalEdgeAllowlist(evidence, patterns)
	keys := make([]string, 0, len(edges))
	for key := range edges {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]map[string]string, 0, len(keys))
	for _, key := range keys {
		parts := strings.SplitN(key, "\x00", 2)
		out = append(out, map[string]string{"from": parts[0], "to": parts[1]})
	}
	return out
}

func flattenCausalPatterns(groups ...[]domain.CausalPattern) []domain.CausalPattern {
	var out []domain.CausalPattern
	for _, group := range groups {
		out = append(out, group...)
	}
	return out
}

func causalNodeAllowlist(evidence []domain.Evidence, patterns []domain.CausalPattern) map[string]struct{} {
	patternNodes := map[string]struct{}{}
	for _, pattern := range patterns {
		if pattern.Status != "active" {
			continue
		}
		for _, node := range pattern.Nodes {
			patternNodes[node.ID] = struct{}{}
		}
	}
	allowed := map[string]struct{}{}
	for _, item := range evidence {
		if item.ID == "" {
			continue
		}
		allowed["obs:"+item.ID] = struct{}{}
		for _, nodeID := range item.CausalNodeIDs {
			if _, isPatternNode := patternNodes[nodeID]; isPatternNode {
				allowed[nodeID] = struct{}{}
			}
		}
	}
	return allowed
}

func causalEdgeAllowlist(evidence []domain.Evidence, patterns []domain.CausalPattern) map[string]struct{} {
	nodes := causalNodeAllowlist(evidence, patterns)
	edges := map[string]struct{}{}
	for _, pattern := range patterns {
		if pattern.Status != "active" {
			continue
		}
		for _, edge := range pattern.Edges {
			if _, fromObserved := nodes[edge.From]; !fromObserved {
				continue
			}
			if _, toObserved := nodes[edge.To]; !toObserved {
				continue
			}
			edges[edge.From+"\x00"+edge.To] = struct{}{}
		}
	}
	return edges
}

func causalPathIsServerValid(expected, supporting []string, edges map[string]struct{}) bool {
	if len(expected) == 0 {
		return false
	}
	if len(expected) == 1 {
		if !strings.HasPrefix(expected[0], "obs:") {
			return true
		}
		return slices.Contains(supporting, strings.TrimPrefix(expected[0], "obs:"))
	}
	for _, nodeID := range expected {
		if strings.HasPrefix(nodeID, "obs:") {
			return false
		}
	}
	for index := 0; index < len(expected)-1; index++ {
		if _, ok := edges[expected[index]+"\x00"+expected[index+1]]; !ok {
			return false
		}
	}
	return true
}

func allEvidenceReferencesKnown(ids []string, allowed map[string]struct{}) bool {
	for _, id := range ids {
		if _, ok := allowed[id]; !ok {
			return false
		}
	}
	return true
}
