package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/cloudwego/eino/components/tool"
	toolutils "github.com/cloudwego/eino/components/tool/utils"
	"github.com/kubepilot-aiops/kubepilot/internal/causal"
	causaldiscovery "github.com/kubepilot-aiops/kubepilot/internal/causal/discovery"
	causalextractor "github.com/kubepilot-aiops/kubepilot/internal/causal/extractor"
	causalknowledge "github.com/kubepilot-aiops/kubepilot/internal/causal/knowledge"
	causalvalidator "github.com/kubepilot-aiops/kubepilot/internal/causal/validator"
	"github.com/kubepilot-aiops/kubepilot/internal/domain"
	rankpolicy "github.com/kubepilot-aiops/kubepilot/internal/reasoning/evidence"
	"github.com/kubepilot-aiops/kubepilot/internal/retrieval/reranker"
	"github.com/kubepilot-aiops/kubepilot/internal/safety"
	"github.com/kubepilot-aiops/kubepilot/internal/topology"
	topologyknowledge "github.com/kubepilot-aiops/kubepilot/internal/topology/knowledge"
	"github.com/kubepilot-aiops/kubepilot/reasoning"
	captools "github.com/kubepilot-aiops/kubepilot/tools"
)

type constrainedToolDeps struct {
	Collectors         map[string]Collector
	Historical         HistoricalCandidateRetriever
	Knowledge          CausalPatternReader
	Reasoning          *reasoning.Engine
	Executor           Executor
	Reranker           reranker.Service
	Policy             *rankpolicy.Policy
	Causal             *causal.Matcher
	GraphStore         topology.GraphStore
	TopologyPatterns   topologyknowledge.Reader
	CausalPatterns     causalknowledge.Reader
	DiscoveredPatterns causaldiscovery.Reader
	Transition         func(context.Context, *domain.Incident, domain.IncidentStatus) error
}

type investigationRequest struct {
	HypothesisID  string `json:"hypothesis_id,omitempty"`
	Service       string `json:"service,omitempty"`
	Resource      string `json:"resource,omitempty"`
	WindowMinutes int    `json:"window_minutes,omitempty"`
}

type boundedLimit struct {
	Limit int `json:"limit,omitempty"`
}

type hypothesisSelection struct {
	HypothesisID string `json:"hypothesis_id"`
}

type emptyToolInput struct{}

type constrainedToolOutput struct {
	OK                      bool                                       `json:"ok"`
	Message                 string                                     `json:"message,omitempty"`
	Evidence                []domain.Evidence                          `json:"evidence,omitempty"`
	Candidates              []domain.RetrievalCandidate                `json:"candidates,omitempty"`
	Patterns                []domain.CausalPattern                     `json:"patterns,omitempty"`
	Verified                []domain.VerifiedHypothesis                `json:"verified,omitempty"`
	Feedback                *domain.SafetyFeedback                     `json:"safety_feedback,omitempty"`
	DryRun                  *domain.DryRunResult                       `json:"dry_run,omitempty"`
	Graph                   *topology.IncidentGraph                    `json:"incident_graph,omitempty"`
	Matches                 []causal.PatternMatch                      `json:"causal_matches,omitempty"`
	CausalScore             *causal.HypothesisScore                    `json:"causal_score,omitempty"`
	TopologyPatterns        []topologyknowledge.ServiceTopologyPattern `json:"topology_patterns,omitempty"`
	CausalKnowledgePatterns []causalknowledge.CausalPattern            `json:"causal_knowledge_patterns,omitempty"`
	DiscoveredPatterns      []causaldiscovery.CausalPatternCandidate   `json:"discovered_causal_patterns,omitempty"`
	CausalProposal          *causalknowledge.Proposal                  `json:"causal_proposal,omitempty"`
	CausalValidation        *causalknowledge.ValidationResult          `json:"causal_validation,omitempty"`
}

func buildConstrainedDiagnosisTools(deps constrainedToolDeps) ([]tool.BaseTool, error) {
	var result []tool.BaseTool
	for _, source := range []string{"metric", "log", "trace", "kubernetes"} {
		source := source
		candidate, err := toolutils.InferTool(evidenceToolName(source), "Collect bounded structured evidence. Filters are constrained to the authoritative Incident namespace and time window.", func(ctx context.Context, in investigationRequest) (constrainedToolOutput, error) {
			return collectConstrainedEvidence(ctx, deps, source, in)
		})
		if err != nil {
			return nil, err
		}
		result = append(result, candidate)
	}

	add := func(name, description string, fn any) error {
		var candidate tool.BaseTool
		var err error
		switch typed := fn.(type) {
		case func(context.Context, emptyToolInput) (constrainedToolOutput, error):
			candidate, err = toolutils.InferTool(name, description, typed)
		case func(context.Context, boundedLimit) (constrainedToolOutput, error):
			candidate, err = toolutils.InferTool(name, description, typed)
		case func(context.Context, HypothesisSubmission) (constrainedToolOutput, error):
			candidate, err = toolutils.InferTool(name, description, typed)
		case func(context.Context, hypothesisSelection) (constrainedToolOutput, error):
			candidate, err = toolutils.InferTool(name, description, typed)
		case func(context.Context, graphBuildRequest) (constrainedToolOutput, error):
			candidate, err = toolutils.InferTool(name, description, typed)
		case func(context.Context, causalPathRequest) (constrainedToolOutput, error):
			candidate, err = toolutils.InferTool(name, description, typed)
		case func(context.Context, causalPatternProposalRequest) (constrainedToolOutput, error):
			candidate, err = toolutils.InferTool(name, description, typed)
		default:
			return fmt.Errorf("unsupported constrained tool %s", name)
		}
		if err == nil {
			result = append(result, candidate)
		}
		return err
	}

	if err := add("rank_incident_evidence", "Rank and bound current evidence with the pinned attribution policy.", func(ctx context.Context, _ emptyToolInput) (constrainedToolOutput, error) {
		runtime, err := runtimeFromContext(ctx)
		if err != nil {
			return constrainedToolOutput{}, err
		}
		runtime.mu.Lock()
		defer runtime.mu.Unlock()
		ranked, err := deps.Reasoning.RankEvidence(runtime.state.Incident, runtime.state.Incident.Evidence)
		if err != nil {
			return safetyObservationLocked(ctx, runtime, DiagnosisAgentName, domain.SafetyScopeDiagnosis, "evidence_requirements_missing", err.Error(), []string{"current evidence does not satisfy attribution and source requirements"})
		}
		runtime.state.RankedEvidence = ranked.Evidence
		mergeLedger(&runtime.state.DiagnosisLedger, ranked.Ledger)
		return constrainedToolOutput{OK: true, Evidence: ranked.Evidence}, nil
	}); err != nil {
		return nil, err
	}

	if deps.Reranker != nil && deps.Reranker.Enabled() {
		if err := add("rerank_incident_evidence", "Use the configured API reranker to score whether current evidence explains this Incident.", func(ctx context.Context, _ emptyToolInput) (constrainedToolOutput, error) {
			return neuralRerankEvidence(ctx, deps)
		}); err != nil {
			return nil, err
		}
	}

	if err := add("build_incident_features", "Build deterministic observed features and dependency attributes from current evidence.", func(ctx context.Context, _ emptyToolInput) (constrainedToolOutput, error) {
		runtime, err := runtimeFromContext(ctx)
		if err != nil {
			return constrainedToolOutput{}, err
		}
		runtime.mu.Lock()
		defer runtime.mu.Unlock()
		items := runtime.state.RankedEvidence
		if len(items) == 0 {
			items = runtime.state.Incident.Evidence
		}
		if deps.Knowledge != nil {
			patterns, loadErr := deps.Knowledge.ListCausalPatterns(ctx, "active")
			if loadErr != nil {
				return constrainedToolOutput{OK: false, Message: "causal knowledge unavailable"}, nil
			}
			items = deps.Reasoning.AnnotateCausalNodes(items, patterns)
		}
		runtime.state.RankedEvidence = items
		runtime.state.Features = deps.Reasoning.BuildFeatures(runtime.state.Incident, items)
		return constrainedToolOutput{OK: true, Evidence: compactToolEvidence(items, 32<<10)}, nil
	}); err != nil {
		return nil, err
	}

	if err := add("build_incident_graph", "Build the current Incident dependency graph from server-owned evidence. The graph is observational and cannot authorize mutations.", func(ctx context.Context, _ graphBuildRequest) (constrainedToolOutput, error) {
		runtime, err := runtimeFromContext(ctx)
		if err != nil {
			return constrainedToolOutput{}, err
		}
		runtime.mu.Lock()
		graph := topology.Build(runtime.state.Incident, runtime.state.Incident.Evidence)
		runtime.state.IncidentGraph = &graph
		runtime.state.Features.TopologyGraph = graph.ToDependencyGraph(runtime.state.Incident.Service)
		if deps.GraphStore != nil {
			if storeErr := deps.GraphStore.Put(ctx, graph); storeErr != nil {
				runtime.state.DiagnosisLedger.InfrastructureErrors = append(runtime.state.DiagnosisLedger.InfrastructureErrors, "incident graph persistence unavailable")
				runtime.mu.Unlock()
				return constrainedToolOutput{OK: false, Graph: &graph, Message: "incident graph built but persistence is unavailable"}, nil
			}
		}
		runtime.mu.Unlock()
		return constrainedToolOutput{OK: true, Graph: &graph, Message: "incident graph built from current observations"}, nil
	}); err != nil {
		return nil, err
	}

	if deps.TopologyPatterns != nil {
		if err := add("retrieve_topology_patterns", "Retrieve bounded reusable topology patterns from resolved Incident knowledge. Concrete pod and IP identities are not returned.", func(ctx context.Context, in boundedLimit) (constrainedToolOutput, error) {
			runtime, err := runtimeFromContext(ctx)
			if err != nil {
				return constrainedToolOutput{}, err
			}
			runtime.mu.Lock()
			graph := topology.IncidentGraph{}
			if runtime.state.IncidentGraph != nil {
				graph = *runtime.state.IncidentGraph
			} else {
				graph = topology.Build(runtime.state.Incident, runtime.state.Incident.Evidence)
			}
			runtime.mu.Unlock()
			limit := in.Limit
			if limit <= 0 || limit > 20 {
				limit = 10
			}
			patterns, callErr := deps.TopologyPatterns.Search(ctx, graph, limit)
			if callErr != nil {
				return constrainedToolOutput{OK: false, Message: "topology knowledge unavailable"}, nil
			}
			return constrainedToolOutput{OK: true, TopologyPatterns: patterns}, nil
		}); err != nil {
			return nil, err
		}
	}

	if deps.CausalPatterns != nil {
		if err := add("retrieve_causal_patterns", "Retrieve bounded validated causal patterns. Patterns are observational knowledge and cannot be modified by the Agent.", func(ctx context.Context, in boundedLimit) (constrainedToolOutput, error) {
			limit := in.Limit
			if limit <= 0 || limit > 20 {
				limit = 10
			}
			patterns, callErr := deps.CausalPatterns.List(ctx, "active", limit)
			if callErr != nil {
				return constrainedToolOutput{OK: false, Message: "causal knowledge unavailable"}, nil
			}
			return constrainedToolOutput{OK: true, CausalKnowledgePatterns: patterns}, nil
		}); err != nil {
			return nil, err
		}
		if err := add("propose_causal_pattern", "Propose a causal pattern from observed Evidence. This returns an unpersisted proposal for deterministic validation.", func(ctx context.Context, in causalPatternProposalRequest) (constrainedToolOutput, error) {
			runtime, err := runtimeFromContext(ctx)
			if err != nil {
				return constrainedToolOutput{}, err
			}
			runtime.mu.Lock()
			incident := *runtime.state.Incident
			runtime.mu.Unlock()
			proposal, ok := causalknowledge.ProposalFromDraft(&incident, in.Cause, in.Path, in.EvidenceIDs, incident.Confidence)
			if !ok {
				return safetyObservation(ctx, runtime, DiagnosisAgentName, domain.SafetyScopeDiagnosis, "causal_proposal_insufficient", "the submitted causal proposal is not grounded in current Incident Evidence", []string{"a causal path and independent observed Evidence are required"})
			}
			runtime.mu.Lock()
			runtime.state.CausalProposal = &proposal
			runtime.mu.Unlock()
			return constrainedToolOutput{OK: true, CausalProposal: &proposal, Message: "proposal is not persisted until validation and service-side merge"}, nil
		}); err != nil {
			return nil, err
		}
		if err := add("validate_causal_pattern", "Validate a causal pattern proposal against current Incident Evidence and knowledge repetition. Validation never writes the knowledge store.", func(ctx context.Context, _ emptyToolInput) (constrainedToolOutput, error) {
			runtime, err := runtimeFromContext(ctx)
			if err != nil {
				return constrainedToolOutput{}, err
			}
			runtime.mu.Lock()
			incident := *runtime.state.Incident
			runtime.mu.Unlock()
			runtime.mu.Lock()
			proposal := causalknowledge.Proposal{}
			if runtime.state.CausalProposal != nil {
				proposal = *runtime.state.CausalProposal
			}
			runtime.mu.Unlock()
			ok := proposal.Pattern.PatternID != ""
			if !ok {
				proposal, ok = causalextractor.Propose(&incident)
			}
			if !ok {
				return safetyObservation(ctx, runtime, DiagnosisAgentName, domain.SafetyScopeDiagnosis, "causal_proposal_insufficient", "there is no grounded causal proposal to validate", []string{"a grounded causal proposal is required"})
			}
			result, callErr := causalvalidator.New(deps.CausalPatterns).Validate(ctx, &incident, proposal)
			if callErr != nil {
				return constrainedToolOutput{OK: false, Message: "causal validation unavailable"}, nil
			}
			if !result.Valid {
				runtime.mu.Lock()
				runtime.state.CausalValidation = &result
				runtime.mu.Unlock()
				return constrainedToolOutput{OK: false, CausalValidation: &result, Message: "causal proposal rejected by deterministic validation"}, nil
			}
			runtime.mu.Lock()
			runtime.state.CausalValidation = &result
			runtime.mu.Unlock()
			return constrainedToolOutput{OK: true, CausalValidation: &result, Message: "causal proposal validated; persistence remains outside Agent permissions"}, nil
		}); err != nil {
			return nil, err
		}
	}

	if deps.DiscoveredPatterns != nil {
		if err := add("retrieve_discovered_causal_patterns", "Retrieve bounded accepted causal patterns discovered from independent resolved Incidents. Results are observational and cannot be modified by the Agent.", func(ctx context.Context, in boundedLimit) (constrainedToolOutput, error) {
			runtime, err := runtimeFromContext(ctx)
			if err != nil {
				return constrainedToolOutput{}, err
			}
			runtime.mu.Lock()
			incident := *runtime.state.Incident
			runtime.mu.Unlock()
			limit := in.Limit
			if limit <= 0 || limit > 20 {
				limit = 10
			}
			terms := []string{incident.RootCause, incident.RootCauseCategory, incident.RootCauseVariant, incident.Service, incident.Resource}
			for _, evidence := range incident.Evidence {
				terms = append(terms, evidence.Summary, evidence.TemplateID)
			}
			patterns, callErr := deps.DiscoveredPatterns.Search(ctx, nonEmptyTerms(terms), limit)
			if callErr != nil {
				return constrainedToolOutput{OK: false, Message: "discovered causal knowledge unavailable"}, nil
			}
			return constrainedToolOutput{OK: true, DiscoveredPatterns: patterns}, nil
		}); err != nil {
			return nil, err
		}
	}

	for _, spec := range []struct {
		name, source string
		call         func(context.Context, domain.IncidentFeatures, int) ([]domain.RetrievalCandidate, error)
	}{
		{"retrieve_semantic_incidents", "semantic", func(ctx context.Context, f domain.IncidentFeatures, k int) ([]domain.RetrievalCandidate, error) {
			return deps.Historical.Semantic(ctx, f, k)
		}},
		{"retrieve_lexical_incidents", "lexical", func(ctx context.Context, f domain.IncidentFeatures, k int) ([]domain.RetrievalCandidate, error) {
			return deps.Historical.Lexical(ctx, f, k)
		}},
		{"retrieve_topology_incidents", "topology", func(ctx context.Context, f domain.IncidentFeatures, k int) ([]domain.RetrievalCandidate, error) {
			return deps.Historical.Topology(ctx, f, k)
		}},
	} {
		spec := spec
		if deps.Historical == nil {
			continue
		}
		if err := add(spec.name, "Retrieve bounded "+spec.source+" historical Incident candidates.", func(ctx context.Context, in boundedLimit) (constrainedToolOutput, error) {
			runtime, err := runtimeFromContext(ctx)
			if err != nil {
				return constrainedToolOutput{}, err
			}
			runtime.mu.Lock()
			features := runtime.state.Features
			runtime.mu.Unlock()
			limit := in.Limit
			if limit <= 0 || limit > 50 {
				limit = 50
			}
			items, callErr := spec.call(ctx, features, limit)
			runtime.mu.Lock()
			defer runtime.mu.Unlock()
			if runtime.state.CandidateLists == nil {
				runtime.state.CandidateLists = map[string][]domain.RetrievalCandidate{}
			}
			if callErr != nil {
				runtime.state.DiagnosisLedger.InfrastructureErrors = append(runtime.state.DiagnosisLedger.InfrastructureErrors, spec.source+" retrieval unavailable")
				return constrainedToolOutput{OK: false, Message: spec.source + " retrieval unavailable"}, nil
			}
			runtime.state.CandidateLists[spec.source] = items
			return constrainedToolOutput{OK: true, Candidates: compactToolCandidates(items, 10)}, nil
		}); err != nil {
			return nil, err
		}
	}

	if err := add("fuse_incident_candidates", "Fuse all currently retrieved candidate sets with weighted reciprocal-rank fusion.", func(ctx context.Context, _ emptyToolInput) (constrainedToolOutput, error) {
		runtime, err := runtimeFromContext(ctx)
		if err != nil {
			return constrainedToolOutput{}, err
		}
		runtime.mu.Lock()
		defer runtime.mu.Unlock()
		lists := runtime.state.CandidateLists
		runtime.state.Candidates = deps.Reasoning.Fuse(reasoning.CandidateLists{Semantic: lists["semantic"], Lexical: lists["lexical"], Topology: lists["topology"]})
		return constrainedToolOutput{OK: true, Candidates: compactToolCandidates(runtime.state.Candidates, 10)}, nil
	}); err != nil {
		return nil, err
	}

	if err := add("rerank_incident_candidates", "Rerank current historical candidates using deterministic features and the optional configured neural API.", func(ctx context.Context, _ emptyToolInput) (constrainedToolOutput, error) {
		return rerankCandidates(ctx, deps)
	}); err != nil {
		return nil, err
	}

	if err := add("match_causal_patterns", "Match active causal patterns against current observed features without inventing missing nodes.", func(ctx context.Context, _ emptyToolInput) (constrainedToolOutput, error) {
		runtime, err := runtimeFromContext(ctx)
		if err != nil {
			return constrainedToolOutput{}, err
		}
		if deps.Knowledge == nil && deps.Causal == nil {
			return constrainedToolOutput{OK: true}, nil
		}
		if deps.Causal != nil {
			runtime.mu.Lock()
			evidence := runtime.state.RankedEvidence
			if len(evidence) == 0 {
				evidence = runtime.state.Incident.Evidence
			}
			matches := deps.Causal.MatchEvidence(evidence)
			runtime.state.CausalMatches = matches
			runtime.mu.Unlock()
			return constrainedToolOutput{OK: true, Matches: matches}, nil
		}
		patterns, err := deps.Knowledge.ListCausalPatterns(ctx, "active")
		if err != nil {
			return constrainedToolOutput{OK: false, Message: "causal knowledge unavailable"}, nil
		}
		runtime.mu.Lock()
		defer runtime.mu.Unlock()
		runtime.state.CausalPatterns = deps.Reasoning.MatchCausalPatterns(runtime.state.Features, runtime.state.RankedEvidence, patterns)
		runtime.state.DiagnosisLedger.CausalPatterns = runtime.state.CausalPatterns
		return constrainedToolOutput{OK: true, Patterns: runtime.state.CausalPatterns}, nil
	}); err != nil {
		return nil, err
	}

	if err := add("expand_causal_path", "Expand a selected causal pattern against the current observations and report missing nodes.", func(ctx context.Context, in causalPathRequest) (constrainedToolOutput, error) {
		runtime, err := runtimeFromContext(ctx)
		if err != nil {
			return constrainedToolOutput{}, err
		}
		if deps.Causal == nil {
			return constrainedToolOutput{OK: false, Message: "causal matcher unavailable"}, nil
		}
		runtime.mu.Lock()
		evidence := append([]domain.Evidence(nil), runtime.state.RankedEvidence...)
		if len(evidence) == 0 {
			evidence = append([]domain.Evidence(nil), runtime.state.Incident.Evidence...)
		}
		runtime.mu.Unlock()
		match, ok := deps.Causal.Expand(in.PatternID, evidence)
		if !ok {
			return safetyObservation(ctx, runtime, DiagnosisAgentName, domain.SafetyScopeDiagnosis, "causal_pattern_not_found", "the requested causal pattern is not available", []string{"a known causal pattern is required"})
		}
		runtime.mu.Lock()
		runtime.state.CausalMatches = append(runtime.state.CausalMatches, match)
		runtime.mu.Unlock()
		return constrainedToolOutput{OK: true, Matches: []causal.PatternMatch{match}}, nil
	}); err != nil {
		return nil, err
	}

	if err := add("score_hypothesis_causality", "Score one hypothesis using observed support, causal coverage, topology, history, prior, and contradiction without trusting model confidence.", func(ctx context.Context, in hypothesisSelection) (constrainedToolOutput, error) {
		runtime, err := runtimeFromContext(ctx)
		if err != nil {
			return constrainedToolOutput{}, err
		}
		if deps.Causal == nil {
			return constrainedToolOutput{OK: false, Message: "causal scorer unavailable"}, nil
		}
		runtime.mu.Lock()
		defer runtime.mu.Unlock()
		for index := range runtime.state.VerifiedHypotheses {
			verified := &runtime.state.VerifiedHypotheses[index]
			if verified.Draft.ID != in.HypothesisID {
				continue
			}
			if len(runtime.state.CausalMatches) > 0 {
				best := runtime.state.CausalMatches[0]
				if best.Coverage > verified.CausalPathCoverage {
					verified.CausalPathCoverage = best.Coverage
				}
				verified.MissingCausalNodes = append([]string(nil), best.MissingNodes...)
				runtime.state.CausalEvidence = append(runtime.state.CausalEvidence, causal.HypothesisCausalEvidence{HypothesisID: in.HypothesisID, CausalPath: append([]string(nil), best.CausalPath...), Coverage: best.Coverage, MissingNodes: append([]string(nil), best.MissingNodes...)})
			}
			score := causal.ScoreHypothesis(causal.ScoreInput{ModelPrior: verified.Draft.PriorProbability, EvidenceSupport: verified.SupportingScore, CausalCoverage: verified.CausalPathCoverage, TopologyMatch: verified.TopologyRelevance, HistoricalSimilarity: verified.HistoricalRelevance, Contradiction: verified.ContradictionScore})
			verified.FinalScore = score.Score
			verified.ConfidenceHistory = append(verified.ConfidenceHistory, domain.HypothesisConfidenceRecord{HypothesisID: in.HypothesisID, Sequence: len(verified.ConfidenceHistory) + 1, Score: score.Score, SupportingScore: verified.SupportingScore, ContradictionScore: verified.ContradictionScore, CausalPathCoverage: verified.CausalPathCoverage, HistoricalRelevance: verified.HistoricalRelevance, TopologyRelevance: verified.TopologyRelevance, ComputedAt: time.Now().UTC()})
			runtime.state.DiagnosisLedger.Verified = runtime.state.VerifiedHypotheses
			return constrainedToolOutput{OK: true, CausalScore: &score, Message: "causal hypothesis score computed from server observations"}, nil
		}
		return safetyObservationLocked(ctx, runtime, DiagnosisAgentName, domain.SafetyScopeDiagnosis, "hypothesis_not_found", "the selected hypothesis is not present in the verified ledger", []string{"a verified hypothesis identity is required"})
	}); err != nil {
		return nil, err
	}

	if err := add("submit_hypotheses", "Record one to three falsifiable hypothesis drafts. Safety feedback is returned for invalid evidence references.", func(ctx context.Context, in HypothesisSubmission) (constrainedToolOutput, error) {
		return recordHypotheses(ctx, in)
	}); err != nil {
		return nil, err
	}

	if err := add("verify_incident_hypotheses", "Verify the current hypothesis ledger against current evidence, causal paths, history, and topology.", func(ctx context.Context, _ emptyToolInput) (constrainedToolOutput, error) {
		return verifyConstrainedHypotheses(ctx, deps)
	}); err != nil {
		return nil, err
	}

	if err := add("submit_diagnosis", "Ask the Safety Controller to accept one supported hypothesis as the root cause.", func(ctx context.Context, in hypothesisSelection) (constrainedToolOutput, error) {
		return submitConstrainedDiagnosis(ctx, in)
	}); err != nil {
		return nil, err
	}

	if err := add("escalate_diagnosis", "Stop autonomous diagnosis when the remaining hypotheses cannot be safely distinguished.", func(ctx context.Context, _ emptyToolInput) (constrainedToolOutput, error) {
		runtime, err := runtimeFromContext(ctx)
		if err != nil {
			return constrainedToolOutput{}, err
		}
		runtime.mu.Lock()
		defer runtime.mu.Unlock()
		feedback := safety.HumanRequired(domain.SafetyScopeDiagnosis, "agent_requested_human", "autonomous diagnosis cannot safely distinguish the remaining hypotheses")
		runtime.state.DiagnosisLedger.SafetyFeedback = append(runtime.state.DiagnosisLedger.SafetyFeedback, feedback)
		_ = runtime.transitionIncident(ctx, domain.StatusNeedsAttention)
		runtime.markDoneLocked(DiagnosisAgentName)
		return constrainedToolOutput{Feedback: &feedback}, nil
	}); err != nil {
		return nil, err
	}
	return registerConstrainedToolSet(context.Background(), result, "diagnosis_react", captools.CategoryDecision)
}

func nonEmptyTerms(values []string) []string {
	out := make([]string, 0, len(values))
	seen := map[string]bool{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func buildConstrainedRecoveryTools(deps constrainedToolDeps) ([]tool.BaseTool, error) {
	proposal, err := toolutils.InferTool("submit_recovery_proposal", "Record one allowed recovery proposal. This tool cannot mutate Kubernetes.", func(ctx context.Context, in RecoveryDecision) (constrainedToolOutput, error) {
		runtime, err := runtimeFromContext(ctx)
		if err != nil {
			return constrainedToolOutput{}, err
		}
		runtime.mu.Lock()
		defer runtime.mu.Unlock()
		switch in.Action {
		case domain.ActionRestartPod, domain.ActionScaleDeployment, domain.ActionRollbackDeployment:
		default:
			feedback := safety.Fatal(domain.SafetyScopeRecoveryProposal, "forbidden_recovery_action", "the requested recovery capability is outside the allowed policy")
			runtime.state.DiagnosisLedger.SafetyFeedback = append(runtime.state.DiagnosisLedger.SafetyFeedback, feedback)
			_ = runtime.transitionIncident(ctx, domain.StatusNeedsAttention)
			runtime.markDoneLocked(RecoveryAgentName)
			return constrainedToolOutput{Feedback: &feedback}, nil
		}
		parts := strings.Split(strings.TrimSpace(in.Target), "/")
		if len(parts) == 2 && parts[0] != runtime.state.Incident.Namespace {
			return fatalRecoveryLocked(ctx, runtime, "namespace_policy_violation", "the proposal target is outside the authoritative Incident namespace")
		}
		proposalText := strings.ToLower(in.Reason + "\n" + in.Risk + "\n" + in.Diff + "\n" + in.Rollback + "\n" + fmt.Sprint(in.Parameters))
		for _, marker := range []string{"kubectl ", "bash ", "sh -c", "#!/bin/sh", "apiversion:", "kind: deployment"} {
			if strings.Contains(proposalText, marker) {
				return fatalRecoveryLocked(ctx, runtime, "free_form_execution_forbidden", "the proposal contains a forbidden free-form execution mechanism")
			}
		}
		p, buildErr := recoveryProposal(runtime.state.Incident, in)
		if buildErr != nil {
			return recoveryRepairableLocked(ctx, runtime, "proposal_target_invalid", buildErr.Error(), []string{"proposal target must match the diagnosed resource"})
		}
		runtime.state.Incident.Proposal = p
		return constrainedToolOutput{OK: true, Message: "proposal recorded for validation and dry-run"}, nil
	})
	if err != nil {
		return nil, err
	}
	dryRun, err := toolutils.InferTool("dry_run_recovery_proposal", "Validate the recorded proposal using Kubernetes DryRunAll. No mutation is performed.", func(ctx context.Context, _ emptyToolInput) (constrainedToolOutput, error) {
		runtime, err := runtimeFromContext(ctx)
		if err != nil {
			return constrainedToolOutput{}, err
		}
		runtime.mu.Lock()
		if err = validateRecoveryProposal(runtime.state.Incident); err != nil {
			runtime.mu.Unlock()
			return recoveryRepairable(ctx, runtime, "proposal_incomplete", err.Error(), []string{"proposal must include a complete risk, diff, and rollback description"})
		}
		incident := *runtime.state.Incident
		runtime.mu.Unlock()
		result, dryErr := dryRunProposal(ctx, deps.Executor, &incident)
		runtime.mu.Lock()
		defer runtime.mu.Unlock()
		if dryErr != nil || result == nil || !result.Success {
			reason := "recovery dry-run did not satisfy current target preconditions"
			if dryErr != nil {
				reason = dryErr.Error()
			}
			return recoveryRepairableLocked(ctx, runtime, "dry_run_failed", reason, []string{"proposal must be consistent with the current target state"})
		}
		runtime.state.DryRun = result
		runtime.state.Incident.DryRun = result
		return constrainedToolOutput{OK: true, DryRun: result}, nil
	})
	if err != nil {
		return nil, err
	}
	accept, err := toolutils.InferTool("accept_recovery_proposal", "Submit the validated and fresh dry-run proposal to the deterministic approval boundary.", func(ctx context.Context, _ emptyToolInput) (constrainedToolOutput, error) {
		runtime, err := runtimeFromContext(ctx)
		if err != nil {
			return constrainedToolOutput{}, err
		}
		runtime.mu.Lock()
		defer runtime.mu.Unlock()
		if runtime.state.Incident.Proposal == nil || runtime.state.DryRun == nil || !runtime.state.DryRun.Success {
			return recoveryRepairableLocked(ctx, runtime, "dry_run_required", "the proposal has no successful current dry-run", []string{"a fresh proposal validation result is required"})
		}
		if time.Since(runtime.state.DryRun.ValidatedAt) > 2*time.Minute {
			return recoveryRepairableLocked(ctx, runtime, "dry_run_expired", "the proposal validation snapshot is no longer current", []string{"the proposal must be validated against current target state"})
		}
		if validator, ok := deps.Executor.(interface {
			ValidateDryRunFreshness(context.Context, *domain.RecoveryProposal, *domain.DryRunResult) error
		}); ok {
			if validateErr := validator.ValidateDryRunFreshness(ctx, runtime.state.Incident.Proposal, runtime.state.DryRun); validateErr != nil {
				return recoveryRepairableLocked(ctx, runtime, "target_state_changed", validateErr.Error(), []string{"the proposal must match a current target-state validation snapshot"})
			}
		}
		runtime.markDoneLocked(RecoveryAgentName)
		return constrainedToolOutput{OK: true, Message: "proposal accepted for deterministic approval interrupt", DryRun: runtime.state.DryRun}, nil
	})
	if err != nil {
		return nil, err
	}
	escalate, err := toolutils.InferTool("escalate_recovery", "Stop autonomous proposal planning when a safe proposal cannot be produced.", func(ctx context.Context, _ emptyToolInput) (constrainedToolOutput, error) {
		runtime, err := runtimeFromContext(ctx)
		if err != nil {
			return constrainedToolOutput{}, err
		}
		runtime.mu.Lock()
		defer runtime.mu.Unlock()
		feedback := safety.HumanRequired(domain.SafetyScopeRecoveryProposal, "agent_requested_human", "a safe recovery proposal cannot be produced within current constraints")
		runtime.state.DiagnosisLedger.SafetyFeedback = append(runtime.state.DiagnosisLedger.SafetyFeedback, feedback)
		_ = runtime.transitionIncident(ctx, domain.StatusNeedsAttention)
		runtime.markDoneLocked(RecoveryAgentName)
		return constrainedToolOutput{Feedback: &feedback}, nil
	})
	if err != nil {
		return nil, err
	}
	return registerConstrainedToolSet(context.Background(), []tool.BaseTool{proposal, dryRun, accept, escalate}, "recovery_react", captools.CategoryDecision)
}

// registerConstrainedToolSet makes the runtime registry, rather than a
// parallel hand-maintained catalog, enforce schema, node allowlist, timeout,
// and payload bounds for every ADK capability. The context passed to this
// helper is only used for schema inspection; invocation receives the caller's
// context through the bounded registry wrapper.
func registerConstrainedToolSet(ctx context.Context, candidates []tool.BaseTool, node string, category captools.ToolCategory) ([]tool.BaseTool, error) {
	registry := captools.NewRegistry()
	for _, candidate := range candidates {
		if err := registry.Register(ctx, candidate, captools.Registration{Category: category, AllowedNodes: []string{node}, Timeout: 2 * time.Minute, MaxArgumentBytes: 128 << 10, MaxOutputBytes: 2 << 20}); err != nil {
			return nil, err
		}
	}
	return registry.ToolsForNode(node)
}

func fatalRecoveryLocked(ctx context.Context, runtime *constrainedRuntime, code, reason string) (constrainedToolOutput, error) {
	feedback := safety.Fatal(domain.SafetyScopeRecoveryProposal, code, reason)
	runtime.state.DiagnosisLedger.SafetyFeedback = append(runtime.state.DiagnosisLedger.SafetyFeedback, feedback)
	_ = runtime.transitionIncident(ctx, domain.StatusNeedsAttention)
	runtime.markDoneLocked(RecoveryAgentName)
	return constrainedToolOutput{Feedback: &feedback}, nil
}

func collectConstrainedEvidence(ctx context.Context, deps constrainedToolDeps, source string, in investigationRequest) (constrainedToolOutput, error) {
	runtime, err := runtimeFromContext(ctx)
	if err != nil {
		return constrainedToolOutput{}, err
	}
	runtime.mu.Lock()
	incident := *runtime.state.Incident
	// Evidence collection is idempotent across ReAct retries and checkpoint
	// resume. Once a source has produced an observation for this Incident, the
	// same tool call returns the persisted observation instead of re-querying an
	// external system. This keeps Resume from repeating collection side effects
	// while still allowing a source that returned no evidence to be retried.
	canonicalSource := map[string]string{"metric": "prometheus", "log": "loki", "trace": "jaeger", "kubernetes": "kubernetes"}[source]
	if canonicalSource != "" {
		cached := make([]domain.Evidence, 0)
		for _, item := range runtime.state.Incident.Evidence {
			if item.Source == canonicalSource {
				cached = append(cached, item)
			}
		}
		if len(cached) > 0 {
			runtime.mu.Unlock()
			return constrainedToolOutput{OK: true, Evidence: compactToolEvidence(cached, 8<<10), Message: "persisted evidence reused"}, nil
		}
	}
	runtime.mu.Unlock()
	if strings.TrimSpace(in.Service) != "" && in.Service != incident.Service {
		return safetyObservation(ctx, runtime, DiagnosisAgentName, domain.SafetyScopeDiagnosis, "filter_outside_incident", "requested evidence filter is outside the authoritative Incident service", []string{"evidence filters must remain attributable to the current Incident"})
	}
	if in.Resource != "" {
		incident.Resource = in.Resource
	}
	window := in.WindowMinutes
	if window <= 0 || window > 15 {
		window = 5
	}
	incident.EvidenceStartAt = time.Now().UTC().Add(-time.Duration(window) * time.Minute)
	collector := deps.Collectors[source]
	if collector == nil {
		return constrainedToolOutput{OK: false, Message: source + " evidence source unavailable"}, nil
	}
	items, collectErr := collector.Collect(ctx, &incident)
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if collectErr != nil {
		runtime.state.DiagnosisLedger.InfrastructureErrors = append(runtime.state.DiagnosisLedger.InfrastructureErrors, source+" evidence unavailable")
		return constrainedToolOutput{OK: false, Message: source + " evidence source unavailable"}, nil
	}
	seen := map[string]bool{}
	for _, item := range runtime.state.Incident.Evidence {
		seen[item.ID] = true
	}
	now := time.Now().UTC()
	added := make([]domain.Evidence, 0, len(items))
	for i := range items {
		item := items[i]
		item.Source = map[string]string{"metric": "prometheus", "log": "loki", "trace": "jaeger", "kubernetes": "kubernetes"}[source]
		if item.WindowStart.IsZero() {
			item.WindowStart = incident.EvidenceStartAt
		}
		if item.WindowEnd.IsZero() {
			item.WindowEnd = now
		}
		normalizeEvidence(&item, runtime.state.Incident)
		if !evidenceInWindow(item, item.WindowStart, item.WindowEnd) {
			continue
		}
		if !seen[item.ID] {
			seen[item.ID] = true
			runtime.state.Incident.Evidence = append(runtime.state.Incident.Evidence, item)
			added = append(added, item)
		}
	}
	if runtime.state.Incident.Status == domain.StatusCollecting {
		_ = runtime.transitionIncident(ctx, domain.StatusDiagnosing)
	}
	return constrainedToolOutput{OK: true, Evidence: compactToolEvidence(added, 8<<10)}, nil
}

func neuralRerankEvidence(ctx context.Context, deps constrainedToolDeps) (constrainedToolOutput, error) {
	runtime, err := runtimeFromContext(ctx)
	if err != nil {
		return constrainedToolOutput{}, err
	}
	runtime.mu.Lock()
	items := append([]domain.Evidence(nil), runtime.state.Incident.Evidence...)
	query := runtime.state.Incident.Summary
	runtime.mu.Unlock()
	if len(items) == 0 {
		return safetyObservation(ctx, runtime, DiagnosisAgentName, domain.SafetyScopeDiagnosis, "no_evidence_to_rerank", "there is no current evidence to attribute", []string{"current observations are required before relevance attribution"})
	}
	docs := make([]string, len(items))
	for i, item := range items {
		docs[i] = item.Summary + " " + fmt.Sprint(item.Content)
	}
	results, callErr := deps.Reranker.Rerank(ctx, query, docs, len(docs))
	if callErr != nil {
		return constrainedToolOutput{OK: false, Message: "reranker service unavailable"}, nil
	}
	for _, result := range results {
		if result.Index >= 0 && result.Index < len(items) {
			items[result.Index].NeuralScore = result.Score
			items[result.Index].NeuralRanked = true
		}
	}
	policy := effectiveRankingPolicy(deps.Policy)
	items = rankpolicy.Rank(policy, runtime.state.Incident, items)
	runtime.mu.Lock()
	runtime.state.Incident.Evidence = items
	runtime.mu.Unlock()
	return constrainedToolOutput{OK: true, Evidence: compactToolEvidence(items, 32<<10)}, nil
}

func rerankCandidates(ctx context.Context, deps constrainedToolDeps) (constrainedToolOutput, error) {
	runtime, err := runtimeFromContext(ctx)
	if err != nil {
		return constrainedToolOutput{}, err
	}
	runtime.mu.Lock()
	features := runtime.state.Features
	items := deps.Reasoning.Rerank(features, runtime.state.Candidates)
	query := strings.Join(features.Terms, " ")
	runtime.mu.Unlock()
	if deps.Reranker != nil && deps.Reranker.Enabled() && len(items) > 0 {
		docs := make([]string, len(items))
		for i, item := range items {
			docs[i] = item.Summary + " " + item.RootCause
		}
		results, callErr := deps.Reranker.Rerank(ctx, query, docs, len(items))
		if callErr == nil {
			for _, r := range results {
				if r.Index >= 0 && r.Index < len(items) {
					items[r.Index].Rank.NeuralSimilarity = r.Score
					items[r.Index].Rank.NeuralRanked = true
				}
			}
		}
	}
	items = rankpolicy.RankCandidates(effectiveRankingPolicy(deps.Policy), items)
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Rank.FinalScore == items[j].Rank.FinalScore {
			return items[i].IncidentID < items[j].IncidentID
		}
		return items[i].Rank.FinalScore > items[j].Rank.FinalScore
	})
	if len(items) > 5 {
		items = items[:5]
	}
	runtime.mu.Lock()
	runtime.state.Candidates = items
	runtime.state.DiagnosisLedger.Candidates = items
	runtime.mu.Unlock()
	return constrainedToolOutput{OK: true, Candidates: compactToolCandidates(items, 5)}, nil
}

func effectiveRankingPolicy(policy *rankpolicy.Policy) rankpolicy.Policy {
	if policy != nil {
		return *policy
	}
	defaultPolicy := rankpolicy.DefaultPolicy()
	return defaultPolicy
}

func compactToolEvidence(items []domain.Evidence, maximumBytes int) []domain.Evidence {
	out := make([]domain.Evidence, 0, min(len(items), 12))
	for _, item := range items {
		candidate := item
		candidate.Data = nil
		candidate.Summary = truncateText(candidate.Summary, 512)
		if len(candidate.RankingReasons) > 4 {
			candidate.RankingReasons = append([]string(nil), candidate.RankingReasons[:4]...)
		}
		if raw, err := json.Marshal(candidate.Content); err == nil && len(raw) > 2048 {
			candidate.Content = map[string]any{"bounded_preview": truncateText(string(raw), 2048)}
		}
		trial := append(append([]domain.Evidence(nil), out...), candidate)
		raw, _ := json.Marshal(trial)
		if maximumBytes > 0 && len(raw) > maximumBytes {
			break
		}
		out = append(out, candidate)
		if len(out) == 12 {
			break
		}
	}
	return out
}

func compactToolCandidates(items []domain.RetrievalCandidate, limit int) []domain.RetrievalCandidate {
	if limit <= 0 || limit > len(items) {
		limit = len(items)
	}
	out := make([]domain.RetrievalCandidate, 0, limit)
	for _, item := range items[:limit] {
		item.Summary = truncateText(item.Summary, 512)
		item.RootCause = truncateText(item.RootCause, 256)
		item.Features.Terms = nil
		item.Features.EvidenceTypes = nil
		item.Features.TraceIDs = nil
		item.Features.TemplateIDs = nil
		item.Features.Observed = nil
		if len(item.RankingReasons) > 4 {
			item.RankingReasons = append([]string(nil), item.RankingReasons[:4]...)
		}
		out = append(out, item)
	}
	return out
}

func truncateText(value string, maximumRunes int) string {
	characters := []rune(value)
	if maximumRunes <= 0 || len(characters) <= maximumRunes {
		return value
	}
	return string(characters[:maximumRunes])
}

func recordHypotheses(ctx context.Context, in HypothesisSubmission) (constrainedToolOutput, error) {
	runtime, err := runtimeFromContext(ctx)
	if err != nil {
		return constrainedToolOutput{}, err
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	missing := []string{}
	if in.ReasoningType != "hypothesis_verification" || len(in.Hypotheses) == 0 || len(in.Hypotheses) > 3 {
		missing = append(missing, "one to three falsifiable hypothesis drafts are required")
	}
	allowed := map[string]bool{}
	for _, item := range runtime.state.Incident.Evidence {
		allowed[item.ID] = true
	}
	for _, draft := range in.Hypotheses {
		if draft.ID == "" || len(draft.SupportingEvidenceIDs) == 0 || len(draft.ExpectedCausalPath) == 0 {
			missing = append(missing, "each hypothesis requires identity, supporting evidence, and an expected causal path")
		}
		for _, id := range append(append([]string{}, draft.SupportingEvidenceIDs...), draft.ContradictingEvidenceIDs...) {
			if !allowed[id] {
				missing = append(missing, "a hypothesis references evidence outside the current allowlist")
			}
		}
		for _, previous := range runtime.state.VerifiedHypotheses {
			if previous.Draft.ID == draft.ID && previous.Status == domain.HypothesisRefuted {
				missing = append(missing, "a refuted hypothesis identity cannot be reused; reconsideration requires a new version")
			}
		}
	}
	if len(missing) > 0 {
		return safetyObservationLocked(ctx, runtime, DiagnosisAgentName, domain.SafetyScopeDiagnosis, "hypothesis_structure_invalid", "the submitted hypothesis set does not satisfy current evidence constraints", missing)
	}
	runtime.state.HypothesisDrafts = in.Hypotheses
	runtime.state.DiagnosisLedger.Drafts = in.Hypotheses
	for _, draft := range in.Hypotheses {
		previousStatus := domain.HypothesisStatus("")
		for _, previous := range runtime.state.VerifiedHypotheses {
			if previous.Draft.ID == draft.ID {
				previousStatus = previous.Status
				break
			}
		}
		switch previousStatus {
		case domain.HypothesisSupported:
			if err := transitionHypothesis(runtime, draft.ID, domain.HypothesisSupported, domain.HypothesisEvidenceSearching, "new observations require verification", "", draft.SupportingEvidenceIDs); err != nil {
				return constrainedToolOutput{}, err
			}
		case domain.HypothesisEvidenceSearching:
			// The draft remains under investigation; do not create a duplicate lifecycle.
		default:
			if err := transitionHypothesis(runtime, draft.ID, "", domain.HypothesisCreated, "hypothesis recorded", "", draft.SupportingEvidenceIDs); err != nil {
				return constrainedToolOutput{}, err
			}
			if err := transitionHypothesis(runtime, draft.ID, domain.HypothesisCreated, domain.HypothesisEvidenceSearching, "investigation started", "", draft.SupportingEvidenceIDs); err != nil {
				return constrainedToolOutput{}, err
			}
		}
	}
	return constrainedToolOutput{OK: true, Message: "hypothesis ledger updated"}, nil
}

func verifyConstrainedHypotheses(ctx context.Context, deps constrainedToolDeps) (constrainedToolOutput, error) {
	runtime, err := runtimeFromContext(ctx)
	if err != nil {
		return constrainedToolOutput{}, err
	}
	runtime.mu.Lock()
	drafts := append([]domain.HypothesisDraft(nil), runtime.state.HypothesisDrafts...)
	previous := append([]domain.VerifiedHypothesis(nil), runtime.state.VerifiedHypotheses...)
	evidence := append([]domain.Evidence(nil), runtime.state.RankedEvidence...)
	if len(evidence) == 0 {
		evidence = append([]domain.Evidence(nil), runtime.state.Incident.Evidence...)
	}
	candidates := append([]domain.RetrievalCandidate(nil), runtime.state.Candidates...)
	patterns := append([]domain.CausalPattern(nil), runtime.state.CausalPatterns...)
	runtime.mu.Unlock()
	if len(drafts) == 0 {
		return safetyObservation(ctx, runtime, DiagnosisAgentName, domain.SafetyScopeDiagnosis, "hypothesis_required", "there is no hypothesis to verify", []string{"a falsifiable hypothesis set must exist before verification"})
	}
	verified, verifyErr := deps.Reasoning.VerifyHypotheses(drafts, evidence, candidates, patterns)
	if verifyErr != nil {
		return safetyObservation(ctx, runtime, DiagnosisAgentName, domain.SafetyScopeDiagnosis, "hypothesis_verification_failed", verifyErr.Error(), []string{"hypothesis evidence references must be current and attributable"})
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	previousByID := make(map[string]domain.VerifiedHypothesis, len(previous))
	for _, item := range previous {
		previousByID[item.Draft.ID] = item
	}
	for index := range verified {
		prior, exists := previousByID[verified[index].Draft.ID]
		if exists && prior.Status == domain.HypothesisRefuted {
			return safetyObservationLocked(ctx, runtime, DiagnosisAgentName, domain.SafetyScopeDiagnosis, "refuted_hypothesis_reused", "a refuted hypothesis identity cannot be reused", []string{"reconsideration of a refuted explanation requires a new hypothesis version"})
		}
		verified[index] = mergeVerificationHistory(prior, verified[index])
	}
	runtime.state.VerifiedHypotheses = verified
	runtime.state.DiagnosisLedger.Verified = verified
	for _, item := range verified {
		from := domain.HypothesisEvidenceSearching
		if prior, exists := previousByID[item.Draft.ID]; exists {
			from = prior.Status
		}
		if item.Status == "" || from == item.Status {
			continue
		}
		if from == domain.HypothesisSupported && item.Status == domain.HypothesisRefuted {
			if err := transitionHypothesis(runtime, item.Draft.ID, from, domain.HypothesisEvidenceSearching, "new contradiction reopened investigation", "", item.VerifiedEvidenceIDs); err != nil {
				return constrainedToolOutput{}, err
			}
			from = domain.HypothesisEvidenceSearching
		}
		if err := transitionHypothesis(runtime, item.Draft.ID, from, item.Status, "verification scores recomputed", "", item.VerifiedEvidenceIDs); err != nil {
			return constrainedToolOutput{}, err
		}
	}
	return constrainedToolOutput{OK: true, Verified: verified}, nil
}

func mergeVerificationHistory(previous, current domain.VerifiedHypothesis) domain.VerifiedHypothesis {
	if len(current.ConfidenceHistory) == 0 {
		return current
	}
	record := current.ConfidenceHistory[len(current.ConfidenceHistory)-1]
	record.Sequence = len(previous.ConfidenceHistory) + 1
	record.AddedEvidenceIDs = stringSetDifference(current.VerifiedEvidenceIDs, previous.VerifiedEvidenceIDs)
	record.RemovedEvidenceIDs = stringSetDifference(previous.VerifiedEvidenceIDs, current.VerifiedEvidenceIDs)
	current.ConfidenceHistory = append(append([]domain.HypothesisConfidenceRecord(nil), previous.ConfidenceHistory...), record)
	return current
}

func stringSetDifference(left, right []string) []string {
	known := make(map[string]bool, len(right))
	for _, item := range right {
		known[item] = true
	}
	var out []string
	for _, item := range left {
		if !known[item] {
			out = append(out, item)
		}
	}
	sort.Strings(out)
	return out
}

func submitConstrainedDiagnosis(ctx context.Context, in hypothesisSelection) (constrainedToolOutput, error) {
	runtime, err := runtimeFromContext(ctx)
	if err != nil {
		return constrainedToolOutput{}, err
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	var selected *domain.VerifiedHypothesis
	for i := range runtime.state.VerifiedHypotheses {
		if runtime.state.VerifiedHypotheses[i].Draft.ID == in.HypothesisID {
			selected = &runtime.state.VerifiedHypotheses[i]
			break
		}
	}
	missing := []string{}
	if selected == nil {
		missing = append(missing, "the selected hypothesis must exist in the verified ledger")
	} else {
		sources := map[string]bool{}
		allowed := map[string]domain.Evidence{}
		for _, item := range runtime.state.Incident.Evidence {
			allowed[item.ID] = item
		}
		hasKube := false
		for _, id := range selected.VerifiedEvidenceIDs {
			if item, ok := allowed[id]; ok {
				sources[item.Source] = true
				if item.Source == "kubernetes" {
					hasKube = true
				}
			}
		}
		if selected.Status != domain.HypothesisSupported {
			missing = append(missing, "the selected hypothesis is not currently supported")
		}
		if selected.FinalScore < .80 {
			missing = append(missing, "the latest confidence remains below the acceptance threshold")
		}
		if selected.ContradictionScore > .10 {
			missing = append(missing, "the latest contradiction score exceeds the acceptance threshold")
		}
		if len(selected.VerifiedEvidenceIDs) < 2 || len(sources) < 2 {
			missing = append(missing, "at least two current evidence records from independent sources are required")
		}
		if !hasKube {
			missing = append(missing, "current Kubernetes evidence is required")
		}
	}
	if len(missing) > 0 {
		return safetyObservationLocked(ctx, runtime, DiagnosisAgentName, domain.SafetyScopeDiagnosis, "diagnosis_insufficient", "the proposed root cause does not satisfy evidence-driven acceptance requirements", missing)
	}
	ranked := rankRootCause(rootRankInput{Verified: runtime.state.VerifiedHypotheses, Evidence: runtime.state.Incident.Evidence})
	if ranked.RequestAdditionalEvidence {
		return safetyObservationLocked(ctx, runtime, DiagnosisAgentName, domain.SafetyScopeDiagnosis, "causal_evidence_incomplete", "the deterministic root-cause ranker requires one bounded supplementary observation", []string{"the selected explanation is missing a causal condition needed for verification"})
	}
	if ranked.NeedsAttention || ranked.Selected == nil {
		return safetyObservationLocked(ctx, runtime, DiagnosisAgentName, domain.SafetyScopeDiagnosis, "root_cause_ranker_rejected", "no verified hypothesis currently satisfies the deterministic root-cause gates", []string{"a supported hypothesis with current independent evidence is required"})
	}
	if ranked.Selected.Draft.ID != in.HypothesisID {
		return safetyObservationLocked(ctx, runtime, DiagnosisAgentName, domain.SafetyScopeDiagnosis, "root_cause_selection_not_ranked", "the submitted selection is not the highest verified candidate under the deterministic ranker", []string{"the submitted root cause must be supported by the current verified ranking"})
	}
	runtime.state.DiagnosisLedger.SelectedHypothesisID = selected.Draft.ID
	if err := transitionVerifiedHypothesis(runtime, selected.Draft.ID, domain.HypothesisSupported, domain.HypothesisAccepted, "root cause accepted", "", selected.VerifiedEvidenceIDs); err != nil {
		return constrainedToolOutput{}, err
	}
	incident := runtime.state.Incident
	incident.ReasoningType = "hypothesis_verification"
	incident.RootCause = selected.Draft.Cause
	incident.RootCauseCategory = selected.Draft.Category
	incident.RootCauseVariant = selected.Draft.Variant
	incident.RootCauseService = selected.Draft.Service
	incident.RootCauseResource = selected.Draft.Resource
	incident.RootCauseEvidenceIDs = append([]string(nil), selected.VerifiedEvidenceIDs...)
	incident.Confidence = selected.FinalScore
	incident.Hypotheses = legacyHypotheses(runtime.state.HypothesisDrafts)
	_ = runtime.transitionIncident(ctx, domain.StatusProposing)
	runtime.markDoneLocked(DiagnosisAgentName)
	return constrainedToolOutput{OK: true, Message: "evidence-driven diagnosis accepted"}, nil
}

func transitionHypothesis(runtime *constrainedRuntime, id string, from, to domain.HypothesisStatus, reason, toolCallID string, evidenceIDs []string) error {
	if runtime.hypotheses != nil {
		return runtime.hypotheses.Transition(id, from, to, reason, toolCallID, evidenceIDs)
	}
	return safety.TransitionHypothesis(&runtime.state.DiagnosisLedger, id, from, to, reason, toolCallID, evidenceIDs)
}

func transitionVerifiedHypothesis(runtime *constrainedRuntime, id string, from, to domain.HypothesisStatus, reason, toolCallID string, evidenceIDs []string) error {
	if runtime.hypotheses != nil {
		return runtime.hypotheses.TransitionVerified(&runtime.state.VerifiedHypotheses, id, from, to, reason, toolCallID, evidenceIDs)
	}
	for index := range runtime.state.VerifiedHypotheses {
		if runtime.state.VerifiedHypotheses[index].Draft.ID == id {
			runtime.state.VerifiedHypotheses[index].Status = to
		}
	}
	return safety.TransitionHypothesis(&runtime.state.DiagnosisLedger, id, from, to, reason, toolCallID, evidenceIDs)
}

func safetyObservation(ctx context.Context, runtime *constrainedRuntime, agent string, scope domain.SafetyScope, code, reason string, missing []string) (constrainedToolOutput, error) {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	return safetyObservationLocked(ctx, runtime, agent, scope, code, reason, missing)
}
func safetyObservationLocked(ctx context.Context, runtime *constrainedRuntime, agent string, scope domain.SafetyScope, code, reason string, missing []string) (constrainedToolOutput, error) {
	remaining, err := runtime.budgets.UseCorrection(agent)
	var feedback domain.SafetyFeedback
	if err != nil {
		feedback = safety.HumanRequired(scope, "correction_budget_exhausted", "the bounded self-correction budget is exhausted")
		_ = runtime.transitionIncident(ctx, domain.StatusNeedsAttention)
		runtime.markDoneLocked(agent)
	} else {
		feedback = safety.Repairable(scope, code, reason, missing, []string{"the current submission must satisfy the missing safety conditions"}, remaining)
	}
	if !safety.ValidateFeedback(feedback, runtime.budgets.KnownTools()) {
		feedback = safety.Repairable(scope, "safety_requirements_incomplete", "the current submission does not satisfy one or more bounded safety requirements", []string{"the current submission must be revised without expanding its authority"}, []string{"the current submission must satisfy the missing safety conditions"}, remaining)
		if err != nil {
			feedback = safety.HumanRequired(scope, "correction_budget_exhausted", "the bounded self-correction budget is exhausted")
		}
	}
	runtime.state.DiagnosisLedger.SafetyFeedback = append(runtime.state.DiagnosisLedger.SafetyFeedback, feedback)
	return constrainedToolOutput{Feedback: &feedback}, nil
}
func recoveryRepairable(ctx context.Context, runtime *constrainedRuntime, code, reason string, missing []string) (constrainedToolOutput, error) {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	return recoveryRepairableLocked(ctx, runtime, code, reason, missing)
}
func recoveryRepairableLocked(ctx context.Context, runtime *constrainedRuntime, code, reason string, missing []string) (constrainedToolOutput, error) {
	return safetyObservationLocked(ctx, runtime, RecoveryAgentName, domain.SafetyScopeRecoveryProposal, code, reason, missing)
}

func mergeLedger(target *domain.DiagnosisLedger, source domain.DiagnosisLedger) {
	target.EvidenceOriginalCount = source.EvidenceOriginalCount
	target.EvidenceRetainedCount = source.EvidenceRetainedCount
	target.EvidenceOriginalBytes = source.EvidenceOriginalBytes
	target.EvidenceRetainedBytes = source.EvidenceRetainedBytes
}
