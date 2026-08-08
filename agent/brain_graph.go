package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	"github.com/kubepilot-aiops/kubepilot/internal/brainruntime"
	"github.com/kubepilot-aiops/kubepilot/internal/domain"
	captools "github.com/kubepilot-aiops/kubepilot/tools"
	"github.com/oklog/ulid/v2"
)

// One Brain turn can traverse termination, context, model, gateway, category
// routing, tool, classification, observation, grounding, belief commit and
// reflection routing before the next turn starts. Eino counts each of those
// synchronous graph levels as a run step. Keep this defensive graph bound
// strictly above the domain Brain budget so MaxTurns, not Eino, owns the
// auditable termination decision. The fixed tail covers graph entry and the
// final termination edge. There is intentionally no wall-clock timeout.
const (
	brainGraphStepsPerTurn = 12
	brainGraphTailSteps    = 16
	BrainGraphMaxSteps     = brainruntime.DefaultMaxTurns*brainGraphStepsPerTurn + brainGraphTailSteps
)

type brainGraphRuntime struct {
	resolver   *BrainSkillResolver
	tools      *captools.Registry
	toolHash   string
	policyHash string
	model      BrainModelRuntime
	deps       brainRuntimeDeps
	states     *sync.Map
}

// BrainModelRuntime is the direct Eino ChatModel boundary for KubePilot. It is
// intentionally independent from AgentRegistry and the removed nested ADK
// agents used by historical baselines.
type BrainModelRuntime struct {
	Chat                     model.BaseChatModel
	InputPricePerMillion     float64
	OutputPricePerMillion    float64
	ReasoningPricePerMillion float64
}

func (r BrainModelRuntime) modelUsage(incidentID, node string, message *schema.Message, duration time.Duration) domain.ModelUsageEvent {
	usage := domain.ModelUsageEvent{IncidentID: incidentID, Agent: node, ParentAgent: "kubepilot_brain", Phase: "brain", DurationMS: duration.Seconds() * 1000, CreatedAt: time.Now().UTC()}
	if message != nil && message.ResponseMeta != nil && message.ResponseMeta.Usage != nil {
		usage.InputTokens = message.ResponseMeta.Usage.PromptTokens
		usage.OutputTokens = message.ResponseMeta.Usage.CompletionTokens
		if usage.OutputTokens == 0 && message.ResponseMeta.Usage.TotalTokens > usage.InputTokens {
			usage.OutputTokens = message.ResponseMeta.Usage.TotalTokens - usage.InputTokens
		}
		usage.ReasoningTokens = message.ResponseMeta.Usage.CompletionTokensDetails.ReasoningTokens
	}
	visibleOutputTokens := max(0, usage.OutputTokens-usage.ReasoningTokens)
	usage.EstimatedCost = (float64(usage.InputTokens)*r.InputPricePerMillion + float64(visibleOutputTokens)*r.OutputPricePerMillion + float64(usage.ReasoningTokens)*r.ReasoningPricePerMillion) / 1_000_000
	return usage
}

type boundBrainModel struct {
	runtime BrainModelRuntime
	tools   []*schema.ToolInfo
	label   string
}

func (m boundBrainModel) Generate(ctx context.Context, messages []*schema.Message, options ...model.Option) (*schema.Message, error) {
	started := time.Now()
	options = append(options, model.WithTools(m.tools), model.WithMaxTokens(8192), model.WithTemperature(0))
	message, err := m.runtime.Chat.Generate(ctx, normalizeBrainModelMessages(messages), options...)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	sanitized, record, persist := normalizeBrainAssistantOutput(message, "", now)
	state, stateErr := brainWorkflowState(ctx)
	if stateErr != nil {
		return nil, stateErr
	}
	record.TurnID = currentBrainTurnID(state)
	if err = recordBrainAssistantTurn(state, record); err != nil {
		return nil, err
	}
	if persist {
		state.BrainMessages = append(state.BrainMessages, sanitized)
	}
	if len(state.BrainTurns) > 0 {
		index := len(state.BrainTurns) - 1
		usage := m.runtime.modelUsage(state.Incident.ID, m.label, message, time.Since(started))
		state.BrainTurns[index].ModelUsage = &usage
		state.BrainTurns[index].CompletedAt = now
		if state.Incident.Investigation != nil {
			state.Incident.Investigation.ModelUsage = append(state.Incident.Investigation.ModelUsage, usage)
			state.Incident.Investigation.AssistantTurns = append([]domain.AssistantTurnRecord(nil), state.AssistantTurns...)
		}
	}
	// The graph router receives a provider-neutral message. For a
	// reasoning-only turn this is deliberately an empty transient Assistant
	// output: the structured output guard records a classified Constraint, but
	// no invalid Assistant message enters conversation or checkpoint state.
	return sanitized, nil
}

func (m boundBrainModel) Stream(ctx context.Context, messages []*schema.Message, options ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	options = append(options, model.WithTools(m.tools), model.WithMaxTokens(8192), model.WithTemperature(0))
	return m.runtime.Chat.Stream(ctx, normalizeBrainModelMessages(messages), options...)
}

func buildBrainGraph(ctx context.Context, runtime *brainGraphRuntime) (compose.AnyGraph, error) {
	graph := compose.NewGraph[*WorkflowState, *WorkflowState]()
	addState := func(name string, handler func(context.Context, *WorkflowState) (*WorkflowState, error)) error {
		return graph.AddLambdaNode(name, compose.InvokableLambda(handler), compose.WithNodeName(name))
	}
	if err := addState("brain_termination_router", runtime.terminationRouter); err != nil {
		return nil, err
	}
	if err := graph.AddLambdaNode("brain_context_builder", compose.InvokableLambda(func(ctx context.Context, state *WorkflowState) ([]*schema.Message, error) {
		return runtime.contextBuilder(ctx, state, false)
	}), compose.WithNodeName("brain_context_builder")); err != nil {
		return nil, err
	}
	if err := graph.AddLambdaNode("reflection_context_builder", compose.InvokableLambda(func(ctx context.Context, state *WorkflowState) ([]*schema.Message, error) {
		return runtime.contextBuilder(ctx, state, true)
	}), compose.WithNodeName("reflection_context_builder")); err != nil {
		return nil, err
	}

	modelNodes := map[domain.BrainToolCategory]string{
		domain.BrainToolEvidence: "brain_model_evidence", domain.BrainToolRetrieval: "brain_model_retrieval",
		domain.BrainToolReasoning: "brain_model_reasoning", domain.BrainToolRecovery: "brain_model_recovery",
		domain.BrainToolControl: "brain_model_control",
	}
	registryNodes := map[domain.BrainToolCategory]string{
		domain.BrainToolEvidence: captools.NodeBrainEvidence, domain.BrainToolRetrieval: captools.NodeBrainRetrieval,
		domain.BrainToolReasoning: captools.NodeBrainReasoning, domain.BrainToolRecovery: captools.NodeBrainRecovery,
		domain.BrainToolControl: captools.NodeBrainControl,
	}
	if err := graph.AddLambdaNode("brain_action_gateway", compose.InvokableLambda(runtime.actionGateway), compose.WithNodeName("brain_action_gateway")); err != nil {
		return nil, err
	}
	for category, key := range modelNodes {
		infos, err := runtime.tools.ToolInfosForNode(ctx, registryNodes[category])
		if err != nil {
			return nil, err
		}
		if err = graph.AddChatModelNode(key, boundBrainModel{runtime: runtime.model, tools: infos, label: key}, compose.WithNodeName("brain_model")); err != nil {
			return nil, err
		}
		if err = graph.AddEdge(key, "brain_action_gateway"); err != nil {
			return nil, err
		}
	}
	reflectionInfos, err := runtime.tools.ToolInfosForNode(ctx, captools.NodeBrainReasoning)
	if err != nil {
		return nil, err
	}
	if err = graph.AddChatModelNode("reflection_update", boundBrainModel{runtime: runtime.model, tools: reflectionInfos, label: "reflection_update"}, compose.WithNodeName("reflection_update")); err != nil {
		return nil, err
	}
	if err = graph.AddEdge("reflection_update", "brain_action_gateway"); err != nil {
		return nil, err
	}
	if err = graph.AddLambdaNode("tool_category_router", compose.InvokableLambda(func(_ context.Context, message *schema.Message) (*schema.Message, error) { return message, nil }), compose.WithNodeName("tool_category_router")); err != nil {
		return nil, err
	}
	if err = graph.AddEdge("brain_action_gateway", "tool_category_router"); err != nil {
		return nil, err
	}
	if err = graph.AddLambdaNode("tool_result_classifier", compose.InvokableLambda(runtime.classifyToolResults), compose.WithNodeName("tool_result_classifier")); err != nil {
		return nil, err
	}
	if err = graph.AddLambdaNode("tool_argument_guard", compose.InvokableLambda(runtime.handleInvalidToolArguments), compose.WithNodeName("tool_argument_guard")); err != nil {
		return nil, err
	}

	toolNodes := map[domain.BrainToolCategory]string{}
	for category, registryNode := range registryNodes {
		config, err := runtime.tools.ToolsNodeConfig(registryNode, category != domain.BrainToolEvidence)
		if err != nil {
			return nil, err
		}
		config.UnknownToolsHandler = func(ctx context.Context, name, _ string) (string, error) {
			state, stateErr := brainWorkflowState(ctx)
			if stateErr != nil {
				return "", stateErr
			}
			output := constraintBrainOutput(newBrainEnvelope(state, name, domain.BrainToolControl, domain.AgentActionIntent{Intent: "unavailable tool requested"}), "tool_not_available_in_category", "requested tool is not exposed in this Tool Category")
			raw, _ := json.Marshal(output)
			return string(raw), nil
		}
		node, err := compose.NewToolNode(ctx, &config)
		if err != nil {
			return nil, err
		}
		key := "tools_" + strings.ToLower(string(category))
		toolNodes[category] = key
		if err = graph.AddToolsNode(key, node, compose.WithNodeName(registryNode)); err != nil {
			return nil, err
		}
		if err = graph.AddEdge(key, "tool_result_classifier"); err != nil {
			return nil, err
		}
	}
	if err = addState("observation_update", runtime.observationUpdate); err != nil {
		return nil, err
	}
	if err = addState("belief_update", runtime.beliefUpdate); err != nil {
		return nil, err
	}
	if err = addState("belief_commit", runtime.beliefCommit); err != nil {
		return nil, err
	}
	if err = addState("reflection_router", func(_ context.Context, state *WorkflowState) (*WorkflowState, error) { return state, nil }); err != nil {
		return nil, err
	}
	if err = graph.AddLambdaNode("structured_output_guard", compose.InvokableLambda(runtime.handleUnstructured), compose.WithNodeName("structured_output_guard")); err != nil {
		return nil, err
	}

	if err = graph.AddEdge(compose.START, "brain_termination_router"); err != nil {
		return nil, err
	}
	if err = graph.AddBranch("brain_termination_router", compose.NewGraphBranch(func(_ context.Context, state *WorkflowState) (string, error) {
		if state.Termination != nil {
			return compose.END, nil
		}
		if state.PendingReflection != nil {
			return "reflection_router", nil
		}
		return "brain_context_builder", nil
	}, map[string]bool{compose.END: true, "brain_context_builder": true, "reflection_router": true})); err != nil {
		return nil, err
	}
	if err = graph.AddBranch("brain_context_builder", compose.NewGraphBranch(func(ctx context.Context, _ []*schema.Message) (string, error) {
		state, stateErr := brainWorkflowState(ctx)
		if stateErr != nil {
			return "", stateErr
		}
		return modelNodes[effectiveToolCategory(state)], nil
	}, graphStringSet(modelNodes))); err != nil {
		return nil, err
	}
	if err = graph.AddEdge("reflection_context_builder", "reflection_update"); err != nil {
		return nil, err
	}
	destinations := map[string]bool{"structured_output_guard": true, "tool_argument_guard": true}
	for _, key := range toolNodes {
		destinations[key] = true
	}
	if err = graph.AddBranch("tool_category_router", compose.NewGraphBranch(func(ctx context.Context, message *schema.Message) (string, error) {
		if message == nil || len(message.ToolCalls) == 0 {
			return "structured_output_guard", nil
		}
		state, stateErr := brainWorkflowState(ctx)
		if stateErr != nil {
			return "", stateErr
		}
		category := effectiveToolCategory(state)
		registryNode := registryNodes[category]
		for _, call := range message.ToolCalls {
			if err := runtime.tools.ValidateArgumentsForNode(registryNode, call.Function.Name, call.Function.Arguments); err != nil {
				return "tool_argument_guard", nil
			}
		}
		return toolNodes[category], nil
	}, destinations)); err != nil {
		return nil, err
	}
	for _, edge := range [][2]string{{"tool_result_classifier", "observation_update"}, {"observation_update", "belief_update"}, {"belief_update", "belief_commit"}, {"belief_commit", "reflection_router"}, {"structured_output_guard", "reflection_router"}, {"tool_argument_guard", "reflection_router"}} {
		if err = graph.AddEdge(edge[0], edge[1]); err != nil {
			return nil, err
		}
	}
	if err = graph.AddBranch("reflection_router", compose.NewGraphBranch(runtime.reflectionRoute, map[string]bool{"reflection_context_builder": true, "brain_termination_router": true})); err != nil {
		return nil, err
	}
	return graph, nil
}

func (r *brainGraphRuntime) contextBuilder(ctx context.Context, state *WorkflowState, reflection bool) ([]*schema.Message, error) {
	if state == nil || state.Incident == nil {
		return nil, fmt.Errorf("Brain context requires WorkflowState and Incident")
	}
	// Normalize the current Workflow Attempt before every model call so hidden
	// reasoning and empty messages can never enter Chat API or checkpoint state.
	// Snapshot migration is handled separately and never reuses an old attempt.
	state.BrainMessages = normalizeBrainModelMessages(state.BrainMessages)
	if r.states != nil {
		r.states.Store(state.Incident.ID, state)
	}
	if state.Incident.Investigation == nil || state.Incident.Investigation.Architecture != "eino-native-self-reflective-brain" {
		state.Incident.Investigation = &domain.Investigation{Architecture: "eino-native-self-reflective-brain", StartedAt: time.Now().UTC()}
	}
	if state.BrainBudget.Limits.MaxTurns == 0 {
		state.BrainBudget = domain.BrainBudgetState{Limits: brainruntime.DefaultBudget()}
	}
	if state.BrainToolPolicy.MaxSameToolRepeat == 0 {
		state.BrainToolPolicy = r.resolver.ToolPolicy()
	}
	if state.BrainPhase == "" {
		state.BrainPhase, state.ActiveToolCategory = domain.BrainPhaseIntake, domain.BrainToolControl
	}
	if brainToolCallBudgetExhausted(state) {
		enterToolBudgetFinalization(state)
		reflection = false
	}
	if reflection {
		if state.BrainPhase != domain.BrainPhaseReflection {
			state.ResumeBrainPhase = state.BrainPhase
		}
		state.BrainPhase = domain.BrainPhaseReflection
	}
	maxOptional := state.BrainBudget.Limits.MaxOptionalSkillsPerTurn
	if method, _ := domain.NormalizeDiagnosisMethod(state.Incident.DiagnosisMethod); method == domain.DiagnosisMethodKubePilotNoOptionalSkills {
		maxOptional = 0
	}
	// Allocate the Turn identity before resolving Skills. Mandatory phase Skills
	// are Runtime-selected for this exact model boundary, so their activation
	// audit must reference the same immutable Turn later used by BrainTurn,
	// AssistantTurn, Tool envelopes, and checkpoints.
	turnID := "turn:" + ulid.Make().String()
	resolved, err := r.resolver.Resolve(state.BrainPhase, state.RequestedSkills, maxOptional, turnID)
	if err != nil {
		return nil, err
	}
	state.ActiveSkillRefs, state.ActiveSkillPrompt = resolved.Refs, resolved.Prompt
	state.ActiveSkillCategories = nil
	for category := range resolved.AllowedCategories {
		state.ActiveSkillCategories = append(state.ActiveSkillCategories, category)
	}
	sort.Slice(state.ActiveSkillCategories, func(i, j int) bool { return state.ActiveSkillCategories[i] < state.ActiveSkillCategories[j] })
	if !categoryAllowed(state.ActiveSkillCategories, state.ActiveToolCategory) {
		state.ActiveToolCategory = defaultCategoryForPhase(state.BrainPhase)
	}
	state.SkillActivations = append(state.SkillActivations, resolved.Activations...)
	turn := domain.BrainTurn{ID: turnID, Sequence: len(state.BrainTurns) + 1, Phase: state.BrainPhase, SkillRefs: append([]domain.SkillRef(nil), resolved.Refs...), ToolCategory: effectiveToolCategory(state), StartedAt: time.Now().UTC()}
	state.BrainTurns = append(state.BrainTurns, turn)
	// Allocate the audit slot while WorkflowState is the graph payload. The
	// ChatModel adapter later updates this existing element instead of appending
	// a new slice header outside the graph's state edge.
	state.AssistantTurns = append(state.AssistantTurns, domain.AssistantTurnRecord{TurnID: turn.ID, ObservedAt: turn.StartedAt})
	state.BrainBudget.Usage.Turns++
	state.EvidenceSnapshotHash = brainruntime.EvidenceSnapshotHash(state.Incident.Evidence)
	currentSnapshot := domain.ExecutionSnapshot{SkillSnapshotHash: r.resolver.SnapshotHash(), ModelConfigHash: state.Incident.ModelConfigHash, ToolSchemaHash: r.toolHash, PolicyHash: r.policyHash}
	if state.ExecutionSnapshot == (domain.ExecutionSnapshot{}) {
		state.ExecutionSnapshot = currentSnapshot
	} else if state.ExecutionSnapshot != currentSnapshot {
		termination, _ := brainruntime.NewTermination(domain.TerminationSafetyBlocked, currentBrainTurnID(state), finalHypothesisID(state), state.EvidenceSnapshotHash, &state.ExecutionSnapshot, []string{"execution snapshot changed; explicit Workflow Attempt migration is required"}, state.BrainBudget)
		state.Termination = &termination
		return nil, fmt.Errorf("execution snapshot changed during Workflow Attempt")
	}
	if state.WorkflowAttempt == nil {
		state.WorkflowAttempt = &domain.WorkflowAttempt{ID: "attempt:" + ulid.Make().String(), IncidentID: state.Incident.ID, Sequence: 1, CheckpointID: "incident:" + state.Incident.ID, Status: domain.WorkflowAttemptActive, ExecutionSnapshot: state.ExecutionSnapshot, StartedAt: time.Now().UTC()}
	}
	state.WorkflowAttempt.EvidenceSnapshotHash = state.EvidenceSnapshotHash
	state.Incident.ExecutionSnapshot = &state.ExecutionSnapshot
	state.Incident.WorkflowAttempt = state.WorkflowAttempt
	state.Incident.SkillSnapshotHash = r.resolver.SnapshotHash()
	skillSelectionPhase := state.BrainPhase
	if state.BrainPhase == domain.BrainPhaseReflection && state.ResumeBrainPhase != "" {
		skillSelectionPhase = state.ResumeBrainPhase
	}
	availableOptionalSkills := []domain.SkillSearchResult(nil)
	if maxOptional > 0 && r.deps.SkillRetrieval != nil {
		search, searchErr := r.deps.SkillRetrieval.Search(ctx, domain.SkillRetrievalQuery{IncidentID: state.Incident.ID, Phase: skillSelectionPhase, Text: brainSkillRetrievalText(state), Documents: r.resolver.SkillDocuments(skillSelectionPhase), Limit: maxOptional * 3})
		if searchErr != nil {
			state.Errors = append(state.Errors, "skill retrieval unavailable")
		} else {
			state.SkillRetrievals = append(state.SkillRetrievals, search)
			availableOptionalSkills = search.Results
		}
	}
	payload := map[string]any{"incident": safeIncident(state.Incident), "world_model": brainWorldModelView(state.WorldModel), "understanding": state.IncidentUnderstanding, "plan": state.InvestigationPlan, "phase": state.BrainPhase, "evidence_snapshot_hash": state.EvidenceSnapshotHash, "evidence_view": brainEvidenceViews(state, state.Incident.Evidence, 24<<10, 8), "hypotheses": state.AgentHypotheses, "admissions": state.HypothesisAdmissions, "evidence_attributions": boundedSlice(state.EvidenceAttributions, 24), "groundings": state.HypothesisGroundings, "grounding_deltas": tailGroundingDeltas(state.GroundingDeltas, 5), "recent_tool_results": tailBrainToolExecutions(state.ToolExecutions, 5), "memory_reads": tailMemoryReads(state.Incident.Investigation.MemoryReads, 5), "hybrid_retrievals": brainHybridRetrievalViews(state.HybridRetrievals, 1), "causal_patterns": tailCausalPatterns(state.BrainCausalPatterns, 6), "loaded_skill_references": state.LoadedSkillReferences, "available_optional_skills": availableOptionalSkills, "optional_skill_selection_phase": skillSelectionPhase, "max_optional_skills_this_turn": maxOptional, "diagnosis": state.AgentDiagnosis, "recovery_plan": state.AgentRecoveryPlan, "budget": state.BrainBudget, "active_tool_category": effectiveToolCategory(state), "execution_snapshot": state.ExecutionSnapshot}
	raw, _ := json.Marshal(payload)
	system := "You are KubePilot's LLM Brain. You own investigation, open-world hypotheses, subjective belief revision, diagnosis selection, and recovery planning. The Runtime owns facts, grounding, constraints, authorization, execution, and verification. Follow the injected Skills exactly. Use only an exposed native structured tool call; do not answer in prose or reveal hidden chain-of-thought. JSON Output Examples in Skills describe native tool calls and must never be emitted as Assistant text. Request optional Skills only by an exact ID listed in available_optional_skills; the Runtime never guesses or auto-selects a Skill for you. Constraint or tool errors are not Incident evidence.\n\n" + resolved.Prompt
	if state.BrainBudget.ToolCallsExhausted {
		system += "\n\nThe investigation ToolCall budget is exhausted. Do not request more evidence, retrieval, hypothesis validation, Skill loading, reflection, or recovery. Use only the existing structured Evidence, Hypotheses, Grounding, and provenance. If no Diagnosis is persisted, call submit_diagnosis with the best supported existing revision or call finish_investigation with HUMAN_ESCALATION when the existing state cannot support one. A persisted Diagnosis must then be passed through validate_diagnosis before finish_investigation. These closing actions do not reopen the ToolCall budget."
	}
	messages := []*schema.Message{schema.SystemMessage(system)}
	messages = append(messages, boundedBrainMessageHistory(state.BrainMessages, 6, 24<<10)...)
	messages = append(messages, schema.UserMessage(string(raw)))
	return messages, nil
}

func (r *brainGraphRuntime) actionGateway(ctx context.Context, message *schema.Message) (*schema.Message, error) {
	state, err := brainWorkflowState(ctx)
	if err != nil {
		return nil, err
	}
	if message == nil {
		return nil, fmt.Errorf("Brain model returned no message")
	}
	if len(message.ToolCalls) > state.BrainBudget.Limits.MaxParallelReadTools && effectiveToolCategory(state) == domain.BrainToolEvidence {
		message.ToolCalls = message.ToolCalls[:state.BrainBudget.Limits.MaxParallelReadTools]
	}
	remaining := state.BrainBudget.Limits.MaxToolCalls - state.BrainBudget.Usage.ToolCalls
	if remaining > 0 && len(message.ToolCalls) > remaining {
		message.ToolCalls = message.ToolCalls[:remaining]
	}
	if remaining <= 0 {
		state.BrainBudget.ToolCallsExhausted = true
		// Keep one model action so the real Eino ToolsNode can return either the
		// permitted closing result or an explicit classified budget Constraint.
		// Never turn exhaustion into an empty Assistant message.
		if len(message.ToolCalls) > 1 {
			message.ToolCalls = message.ToolCalls[:1]
		}
	}
	return message, nil
}

func (r *brainGraphRuntime) reflectionRoute(_ context.Context, state *WorkflowState) (string, error) {
	// Reflection is a Brain turn too. A pending reflection used to route
	// directly back to reflection_context_builder without passing through the
	// termination router, so a stream of corrective reflections could exceed
	// MaxTurns and eventually fail at Eino's defensive graph-step limit. Keep
	// the graph-step limit as a last-resort guard, but make the domain budget the
	// authoritative, auditable termination boundary.
	if state.Termination == nil && state.BrainBudget.Limits.MaxTurns > 0 && state.BrainBudget.Usage.Turns >= state.BrainBudget.Limits.MaxTurns {
		termination, _ := brainruntime.NewTermination(domain.TerminationBudgetExhausted, currentBrainTurnID(state), finalHypothesisID(state), state.EvidenceSnapshotHash, &state.ExecutionSnapshot, unresolvedGaps(state), state.BrainBudget)
		state.Termination = &termination
		state.PendingReflection = nil
		return "brain_termination_router", nil
	}
	if state.Termination == nil && brainToolCallBudgetExhausted(state) {
		// Tool exhaustion closes investigation, not cognition. Skip any pending
		// reflection and give the Brain a bounded structured finalization turn
		// over the evidence already collected.
		enterToolBudgetFinalization(state)
		return "brain_termination_router", nil
	}
	if state.PendingReflection != nil && state.Termination == nil {
		if method, _ := domain.NormalizeDiagnosisMethod(state.Incident.DiagnosisMethod); method == domain.DiagnosisMethodKubePilotNoReflection {
			state.PendingReflection = nil
			return "brain_termination_router", nil
		}
		cost := brainruntime.ReflectionCost(*state.PendingReflection)
		if state.BrainBudget.Usage.ReflectionCostUnits+cost <= state.BrainBudget.Limits.MaxReflectionCostUnits {
			state.BrainBudget.Usage.ReflectionCostUnits += cost
			record := domain.ReflectionRecord{ID: "reflection:" + ulid.Make().String(), Trigger: *state.PendingReflection, CostUnits: cost, OccurredAt: time.Now().UTC()}
			if len(state.ToolExecutions) > 0 {
				last := state.ToolExecutions[len(state.ToolExecutions)-1]
				record.TriggerToolCallID = last.Result.Provenance.ToolCallID
				record.EvidenceIDs = append([]string(nil), last.Result.Provenance.EvidenceIDs...)
				record.HypothesisRevisionIDs = append([]string(nil), last.Envelope.Intent.HypothesisIDs...)
			}
			state.Reflections = append(state.Reflections, record)
			return "reflection_context_builder", nil
		}
		if *state.PendingReflection == domain.ReflectionRecoveryFailure || *state.PendingReflection == domain.ReflectionVerificationFail {
			termination, _ := brainruntime.NewTermination(domain.TerminationSafetyBlocked, currentBrainTurnID(state), finalHypothesisID(state), state.EvidenceSnapshotHash, &state.ExecutionSnapshot, []string{"reflection budget exhausted after recovery or verification failure"}, state.BrainBudget)
			state.Termination = &termination
		}
		state.PendingReflection = nil
	}
	return "brain_termination_router", nil
}

func brainToolCallBudgetExhausted(state *WorkflowState) bool {
	return state != nil && state.BrainBudget.Limits.MaxToolCalls > 0 && state.BrainBudget.Usage.ToolCalls >= state.BrainBudget.Limits.MaxToolCalls
}

func enterToolBudgetFinalization(state *WorkflowState) {
	if state == nil {
		return
	}
	state.BrainBudget.ToolCallsExhausted = true
	state.PendingReflection = nil
	state.RequestedSkills = nil
	if state.AgentDiagnosis == nil {
		state.BrainPhase = domain.BrainPhaseDiagnosis
		state.ActiveToolCategory = domain.BrainToolReasoning
		return
	}
	state.BrainPhase = domain.BrainPhaseEscalation
	state.ActiveToolCategory = domain.BrainToolControl
}

func isToolBudgetClosingAction(toolName string) bool {
	switch toolName {
	case "submit_diagnosis", "validate_diagnosis", "finish_investigation":
		return true
	default:
		return false
	}
}

func effectiveToolCategory(state *WorkflowState) domain.BrainToolCategory {
	switch state.BrainPhase {
	case domain.BrainPhaseIntake, domain.BrainPhaseEscalation, domain.BrainPhaseVerification:
		return domain.BrainToolControl
	case domain.BrainPhasePlanning, domain.BrainPhaseDiagnosis, domain.BrainPhaseReflection:
		return domain.BrainToolReasoning
	case domain.BrainPhaseRecovery:
		return domain.BrainToolRecovery
	}
	if state.ActiveToolCategory == "" {
		return domain.BrainToolReasoning
	}
	return state.ActiveToolCategory
}

func defaultCategoryForPhase(phase domain.BrainPhase) domain.BrainToolCategory {
	switch phase {
	case domain.BrainPhaseIntake, domain.BrainPhaseEscalation, domain.BrainPhaseVerification:
		return domain.BrainToolControl
	case domain.BrainPhaseRecovery:
		return domain.BrainToolRecovery
	default:
		return domain.BrainToolReasoning
	}
}

func categoryAllowed(values []domain.BrainToolCategory, wanted domain.BrainToolCategory) bool {
	if wanted == domain.BrainToolControl {
		return true
	}
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func graphStringSet(values map[domain.BrainToolCategory]string) map[string]bool {
	out := map[string]bool{}
	for _, value := range values {
		out[value] = true
	}
	return out
}

func tailGroundingDeltas(values []domain.GroundingDelta, limit int) []domain.GroundingDelta {
	if len(values) <= limit {
		return values
	}
	return values[len(values)-limit:]
}

func tailBrainToolExecutions(values []domain.BrainToolExecution, limit int) []domain.BrainToolExecution {
	if len(values) <= limit {
		return values
	}
	return values[len(values)-limit:]
}

func tailMemoryReads(values []domain.MemoryAccessEvent, limit int) []domain.MemoryAccessEvent {
	if limit <= 0 || len(values) <= limit {
		return append([]domain.MemoryAccessEvent(nil), values...)
	}
	return append([]domain.MemoryAccessEvent(nil), values[len(values)-limit:]...)
}

func brainSkillRetrievalText(state *WorkflowState) string {
	if state == nil || state.Incident == nil {
		return ""
	}
	parts := []string{state.Incident.Summary, state.Incident.Service, state.Incident.Resource, string(state.BrainPhase)}
	if state.InvestigationPlan != nil {
		parts = append(parts, state.InvestigationPlan.Objective)
	}
	for _, hypothesis := range state.AgentHypotheses {
		if hypothesis.Status != domain.HypothesisRefuted {
			parts = append(parts, hypothesis.Statement, hypothesis.Mechanism)
		}
	}
	if state.WorldModel != nil {
		for _, signal := range state.WorldModel.AbnormalSignals {
			parts = append(parts, signal.Category, signal.Signal, signal.Direction)
		}
	}
	return strings.Join(parts, " ")
}

func tailCausalPatterns(values []domain.CausalPattern, limit int) []domain.CausalPattern {
	if len(values) <= limit {
		return values
	}
	return values[len(values)-limit:]
}

func boundedBrainMessageHistory(values []*schema.Message, maxItems, maxBytes int) []*schema.Message {
	if maxItems <= 0 || maxBytes <= 0 || len(values) == 0 {
		return nil
	}
	units := completeBrainMessageUnits(values)
	selectedUnits := make([][]*schema.Message, 0, len(units))
	used := 0
	items := 0
	for index := len(units) - 1; index >= 0; index-- {
		unit := units[index]
		if len(unit) == 0 || items+len(unit) > maxItems {
			break
		}
		size := 0
		for _, message := range unit {
			size += brainMessageSize(message)
		}
		if used+size > maxBytes {
			break
		}
		used += size
		items += len(unit)
		selectedUnits = append(selectedUnits, unit)
	}
	selected := make([]*schema.Message, 0, items)
	for index := len(selectedUnits) - 1; index >= 0; index-- {
		selected = append(selected, selectedUnits[index]...)
	}
	return selected
}

func normalizeBrainModelMessages(values []*schema.Message) []*schema.Message {
	out := make([]*schema.Message, 0, len(values))
	for _, value := range values {
		if value == nil {
			continue
		}
		copyMessage := *value
		copyMessage.ToolCalls = slices.Clone(value.ToolCalls)
		copyMessage.ReasoningContent = ""
		if copyMessage.Role == schema.Assistant {
			if strings.TrimSpace(copyMessage.Content) == "" && len(copyMessage.ToolCalls) == 0 {
				continue
			}
			// A provider can stop at its output-token boundary after emitting a
			// ToolCall name/ID but before completing the JSON arguments. The
			// Runtime still closes that call with a classified REJECTED Tool
			// result, but replaying the malformed argument fragment makes some
			// Chat APIs reject the entire next request. Preserve the original
			// fragment in the WorkflowState/audit hash and repair only this
			// provider-input copy to a valid JSON object. The paired Tool result
			// remains the authoritative execution status.
			normalizeBrainToolCallArgumentsForProvider(&copyMessage)
			ensureBrainAssistantToolCallContent(&copyMessage)
		}
		if strings.TrimSpace(copyMessage.Content) == "" && copyMessage.Role == schema.Tool {
			copyMessage.Content = `{"class":"ERROR","status":"ERROR","summary":"tool returned an empty result payload"}`
		}
		out = append(out, &copyMessage)
	}
	return out
}

func normalizeBrainToolCallArgumentsForProvider(message *schema.Message) {
	if message == nil || message.Role != schema.Assistant {
		return
	}
	for index := range message.ToolCalls {
		raw := strings.TrimSpace(message.ToolCalls[index].Function.Arguments)
		var object map[string]json.RawMessage
		if raw != "" && json.Unmarshal([]byte(raw), &object) == nil && object != nil {
			continue
		}
		message.ToolCalls[index].Function.Arguments = `{}`
	}
}

func normalizeBrainAssistantOutput(message *schema.Message, turnID string, observedAt time.Time) (*schema.Message, domain.AssistantTurnRecord, bool) {
	record := domain.AssistantTurnRecord{TurnID: turnID, ObservedAt: observedAt}
	if message == nil {
		return &schema.Message{Role: schema.Assistant}, record, false
	}
	record.ContentPresent = strings.TrimSpace(message.Content) != ""
	record.ToolCallPresent = len(message.ToolCalls) > 0
	record.ReasoningPresent = strings.TrimSpace(message.ReasoningContent) != ""
	record.Persisted = record.ContentPresent || record.ToolCallPresent
	sanitized := *message
	sanitized.ReasoningContent = ""
	if sanitized.Role == "" {
		sanitized.Role = schema.Assistant
	}
	if record.ToolCallPresent {
		ensureBrainAssistantToolCallContent(&sanitized)
	}
	return &sanitized, record, record.Persisted
}

func recordBrainAssistantTurn(state *WorkflowState, record domain.AssistantTurnRecord) error {
	if state == nil {
		return fmt.Errorf("Assistant turn audit requires WorkflowState")
	}
	for index := len(state.AssistantTurns) - 1; index >= 0; index-- {
		if state.AssistantTurns[index].TurnID == record.TurnID {
			state.AssistantTurns[index] = record
			return nil
		}
	}
	return fmt.Errorf("Assistant turn audit slot %q was not allocated by the Brain context builder", record.TurnID)
}

func ensureBrainAssistantToolCallContent(message *schema.Message) {
	if message == nil || message.Role != schema.Assistant || strings.TrimSpace(message.Content) != "" {
		return
	}
	if len(message.ToolCalls) == 0 {
		return
	}
	type callSummary struct {
		ID                  string   `json:"id"`
		Tool                string   `json:"tool"`
		Intent              string   `json:"intent,omitempty"`
		ExpectedObservation []string `json:"expected_observation,omitempty"`
	}
	projection := struct {
		Type  string        `json:"type"`
		Calls []callSummary `json:"calls"`
	}{Type: "assistant_tool_calls"}
	for _, call := range message.ToolCalls {
		var intent struct {
			Intent              string   `json:"intent"`
			ExpectedObservation []string `json:"expected_observation"`
		}
		_ = json.Unmarshal([]byte(call.Function.Arguments), &intent)
		projection.Calls = append(projection.Calls, callSummary{ID: call.ID, Tool: call.Function.Name, Intent: intent.Intent, ExpectedObservation: intent.ExpectedObservation})
	}
	raw, _ := json.Marshal(projection)
	message.Content = string(raw)
}

func completeBrainMessageUnits(values []*schema.Message) [][]*schema.Message {
	units := make([][]*schema.Message, 0, len(values))
	for index := 0; index < len(values); {
		if values[index] == nil {
			index++
			continue
		}
		message := *values[index]
		message.ReasoningContent = ""
		if message.Role == schema.Assistant && strings.TrimSpace(message.Content) == "" && len(message.ToolCalls) == 0 {
			index++
			continue
		}
		ensureBrainAssistantToolCallContent(&message)
		if message.Role == schema.Tool {
			// Never expose an orphan Tool result without the Assistant request it
			// answers; the structured Runtime summary remains in the state payload.
			index++
			continue
		}
		unit := []*schema.Message{&message}
		if message.Role == schema.Assistant && len(message.ToolCalls) > 0 {
			allowedCallIDs := map[string]bool{}
			for _, call := range message.ToolCalls {
				allowedCallIDs[call.ID] = true
			}
			cursor := index + 1
			for cursor < len(values) && values[cursor] != nil && values[cursor].Role == schema.Tool {
				if !allowedCallIDs[values[cursor].ToolCallID] {
					break
				}
				toolResult := *values[cursor]
				toolResult.ReasoningContent = ""
				if strings.TrimSpace(toolResult.Content) == "" {
					toolResult.Content = `{"class":"ERROR","status":"ERROR","summary":"tool returned an empty result payload"}`
				}
				unit = append(unit, &toolResult)
				cursor++
			}
			// A request is useful history only after every Tool Call has a result.
			if len(unit)-1 == len(message.ToolCalls) {
				units = append(units, unit)
			}
			index = cursor
			continue
		}
		units = append(units, unit)
		index++
	}
	return units
}

func brainMessageSize(message *schema.Message) int {
	if message == nil {
		return 0
	}
	if len(message.Content) > 24<<10 {
		message.Content = message.Content[:24<<10] + "\n[tool result projection truncated]"
	}
	size := len(message.Content)
	for _, call := range message.ToolCalls {
		size += len(call.ID) + len(call.Function.Name) + len(call.Function.Arguments)
	}
	return size
}
