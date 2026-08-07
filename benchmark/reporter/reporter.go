package reporter

import (
	"bufio"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/kubepilot-aiops/kubepilot/benchmark/scorer"
	artifactlayout "github.com/kubepilot-aiops/kubepilot/internal/artifacts"
)

type Manifest struct {
	ManifestHash        string             `json:"manifest_hash,omitempty"`
	RunID               string             `json:"run_id"`
	Profile             string             `json:"profile"`
	CatalogHash         string             `json:"catalog_hash"`
	Protocol            string             `json:"chat_protocol"`
	Model               string             `json:"chat_model"`
	ModelProfile        string             `json:"model_profile,omitempty"`
	SemanticJudge       bool               `json:"semantic_judge,omitempty"`
	SemanticJudgeModel  string             `json:"semantic_judge_model,omitempty"`
	SemanticJudgeConfig string             `json:"semantic_judge_config_hash,omitempty"`
	EndpointHash        string             `json:"endpoint_hash"`
	ModelConfigHash     string             `json:"model_config_hash"`
	SkillSnapshotHash   string             `json:"skill_snapshot_hash,omitempty"`
	RankingPolicyHash   string             `json:"ranking_policy_hash,omitempty"`
	ToolCostPolicyHash  string             `json:"tool_cost_policy_hash,omitempty"`
	BudgetConfigHash    string             `json:"budget_config_hash,omitempty"`
	RerankerModel       string             `json:"reranker_model,omitempty"`
	RerankerConfigHash  string             `json:"reranker_config_hash,omitempty"`
	EmbeddingModel      string             `json:"embedding_model,omitempty"`
	EmbeddingDimensions string             `json:"embedding_dimensions,omitempty"`
	DiagnosisMethod     string             `json:"diagnosis_method,omitempty"`
	CausalMode          string             `json:"causal_mode,omitempty"`
	Strategies          []string           `json:"strategies,omitempty"`
	DatasetSplit        string             `json:"dataset_split,omitempty"`
	Seeds               []int64            `json:"seeds,omitempty"`
	Repetitions         int                `json:"repetitions,omitempty"`
	Architecture        string             `json:"architecture,omitempty"`
	Parallelism         int                `json:"parallelism,omitempty"`
	ModelConcurrency    int                `json:"model_concurrency,omitempty"`
	WorkerNamespaces    []string           `json:"worker_namespaces,omitempty"`
	ShardPolicy         string             `json:"shard_policy,omitempty"`
	PricingSnapshot     map[string]float64 `json:"pricing_snapshot,omitempty"`
	GitCommit           string             `json:"git_commit"`
	SourceHash          string             `json:"source_hash"`
	HistoryDatasetHash  string             `json:"history_dataset_hash,omitempty"`
	HistoryCollection   string             `json:"history_collection,omitempty"`
	Seed                int64              `json:"seed"`
	StartedAt           time.Time          `json:"started_at"`
	FinishedAt          time.Time          `json:"finished_at,omitempty"`
}

// WriteManifestDir writes a manifest to an explicitly selected logical run
// directory.
func WriteManifestDir(dir string, manifest Manifest) error {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	return writeJSON(filepath.Join(dir, "manifest.json"), manifest)
}

type CaseResult struct {
	CaseID                      string        `json:"case_id"`
	Seed                        int64         `json:"seed"`
	Repetition                  int           `json:"repetition"`
	DatasetSplit                string        `json:"dataset_split,omitempty"`
	WorkerID                    string        `json:"worker_id,omitempty"`
	Namespace                   string        `json:"namespace,omitempty"`
	IncidentID                  string        `json:"incident_id,omitempty"`
	Category                    string        `json:"category"`
	Status                      string        `json:"status"`
	Score                       scorer.Score  `json:"score"`
	Duration                    time.Duration `json:"duration"`
	Error                       string        `json:"error,omitempty"`
	CaseRestarts                int           `json:"case_restarts,omitempty"`
	RootCauseCategory           string        `json:"root_cause_category,omitempty"`
	RootCauseVariant            string        `json:"root_cause_variant,omitempty"`
	Service                     string        `json:"service,omitempty"`
	Resource                    string        `json:"resource,omitempty"`
	Confidence                  float64       `json:"confidence"`
	DiagnosisMethod             string        `json:"diagnosis_method,omitempty"`
	CausalMode                  string        `json:"causal_mode,omitempty"`
	AgentIterations             int           `json:"agent_iterations"`
	AgentToolUses               int           `json:"agent_tool_uses"`
	AgentToolCost               int           `json:"agent_tool_cost"`
	AgentTokens                 int           `json:"agent_tokens"`
	InputTokens                 int           `json:"input_tokens"`
	OutputTokens                int           `json:"output_tokens"`
	ReasoningTokens             int           `json:"reasoning_tokens"`
	EstimatedModelCost          float64       `json:"estimated_model_cost"`
	Architecture                string        `json:"architecture,omitempty"`
	PlannerTasks                int           `json:"planner_tasks"`
	WorkerFindings              int           `json:"worker_findings"`
	DebateRounds                int           `json:"debate_rounds"`
	MemoryReads                 int           `json:"memory_reads"`
	AgentCorrections            int           `json:"agent_corrections"`
	SafetyRejections            int           `json:"safety_rejections"`
	SelfCorrectionAttempts      int           `json:"self_correction_attempts"`
	SelfCorrectionSucceeded     bool          `json:"self_correction_succeeded"`
	HypothesisCount             int           `json:"hypothesis_count"`
	HypothesisConverged         bool          `json:"hypothesis_converged"`
	EvidenceQueries             int           `json:"evidence_queries"`
	EvidenceEfficiency          float64       `json:"evidence_efficiency"`
	IndependentEvidenceRequests int           `json:"independent_evidence_requests"`
	NewEvidenceIDs              int           `json:"new_evidence_ids"`
	ConvergenceRounds           int           `json:"convergence_rounds"`
	CognitiveProposals          int           `json:"cognitive_proposals"`
	CognitiveAcceptedProposals  int           `json:"cognitive_accepted_proposals"`
	CognitiveUsefulProposals    int           `json:"cognitive_useful_proposals"`
	CognitiveRejectedProposals  int           `json:"cognitive_rejected_proposals"`
	ConfidenceUpdates           int           `json:"confidence_updates"`
	AttributedEvidence          int           `json:"attributed_evidence"`
	TopologyCandidates          int           `json:"topology_candidates"`
	RecoveryProposed            bool          `json:"recovery_proposed"`
	ApprovalRequested           bool          `json:"approval_requested"`
	ApprovalGranted             bool          `json:"approval_granted"`
	RecoveryExecuted            bool          `json:"recovery_executed"`
	VerificationOK              bool          `json:"verification_ok"`
	SafetyBlocked               bool          `json:"safety_blocked"`
	DryRunSuccess               bool          `json:"dry_run_success"`
	RecoveryDurationMS          float64       `json:"recovery_duration_ms"`
	SafetyViolation             bool          `json:"safety_violation"`
	ApprovalBypass              bool          `json:"approval_bypass"`
	NamespaceViolation          bool          `json:"namespace_violation"`
	DuplicateMutation           bool          `json:"duplicate_mutation"`
	InfrastructureFailure       bool          `json:"infrastructure_failure"`
	ArbitrationGateFailures     []string      `json:"arbitration_gate_failures,omitempty"`
	JudgeError                  string        `json:"judge_error,omitempty"`
}
type Summary struct {
	Total                           int     `json:"total"`
	Passed                          int     `json:"passed"`
	Failed                          int     `json:"failed"`
	DiagnosisFailures               int     `json:"diagnosis_failures"`
	InfrastructureFailures          int     `json:"infrastructure_failures"`
	InfrastructureFailureRate       float64 `json:"infrastructure_failure_rate"`
	SemanticJudgedCases             int     `json:"semantic_judged_cases"`
	SemanticJudgeFailures           int     `json:"semantic_judge_failures"`
	SemanticRootCauseAccuracy       float64 `json:"semantic_root_cause_accuracy"`
	Valid                           bool    `json:"valid"`
	RootCauseAccuracy               float64 `json:"root_cause_accuracy"`
	RootCauseLocalizationAccuracy   float64 `json:"root_cause_localization_accuracy"`
	CategoryAccuracy                float64 `json:"category_accuracy"`
	VariantAccuracy                 float64 `json:"variant_accuracy"`
	ServiceAccuracy                 float64 `json:"service_accuracy"`
	ResourceAccuracy                float64 `json:"resource_accuracy"`
	EvidencePrecision               float64 `json:"evidence_precision"`
	EvidenceRecall                  float64 `json:"evidence_recall"`
	EvidenceGroundedness            float64 `json:"evidence_groundedness"`
	DecisionAccuracy                float64 `json:"decision_accuracy"`
	ConfidenceBrierScore            float64 `json:"confidence_brier_score"`
	ConfidenceECE                   float64 `json:"confidence_ece"`
	HighConfidenceErrorRate         float64 `json:"high_confidence_error_rate"`
	CategoryMacroF1                 float64 `json:"category_macro_f1"`
	MeanDurationSeconds             float64 `json:"mean_duration_seconds"`
	P95LatencySeconds               float64 `json:"p95_latency_seconds"`
	RecoverySuccess                 float64 `json:"recovery_success"`
	SafetyViolations                int     `json:"safety_violations"`
	MeanModelCost                   float64 `json:"mean_model_cost"`
	MeanInputTokens                 float64 `json:"mean_input_tokens"`
	MeanOutputTokens                float64 `json:"mean_output_tokens"`
	MeanReasoningTokens             float64 `json:"mean_reasoning_tokens"`
	MeanAgentIterations             float64 `json:"mean_agent_iterations"`
	MeanAgentToolUses               float64 `json:"mean_agent_tool_uses"`
	MeanAgentToolCost               float64 `json:"mean_agent_tool_cost"`
	MeanAgentTokens                 float64 `json:"mean_agent_tokens"`
	MeanAgentCorrections            float64 `json:"mean_agent_corrections"`
	TotalSafetyRejections           int     `json:"total_safety_rejections"`
	MeanSafetyRejections            float64 `json:"mean_safety_rejections"`
	SelfCorrectionCases             int     `json:"self_correction_cases"`
	SelfCorrectionSuccesses         int     `json:"self_correction_successes"`
	SelfCorrectionSuccessRate       float64 `json:"self_correction_success_rate"`
	HypothesisConvergenceRate       float64 `json:"hypothesis_convergence_rate"`
	MeanHypothesisCount             float64 `json:"mean_hypothesis_count"`
	MeanEvidenceQueries             float64 `json:"mean_evidence_queries"`
	MeanEvidenceEfficiency          float64 `json:"mean_evidence_efficiency"`
	MeanIndependentEvidenceRequests float64 `json:"mean_independent_evidence_requests"`
	MeanNewEvidenceIDs              float64 `json:"mean_new_evidence_ids"`
	MeanConvergenceRounds           float64 `json:"mean_convergence_rounds"`
	CorrectRCAperEvidenceRequest    float64 `json:"correct_rca_per_evidence_request"`
	MedianEvidenceRequestsCorrect   float64 `json:"median_evidence_requests_correct"`
	CognitiveProposals              int     `json:"cognitive_proposals"`
	CognitiveAcceptedProposals      int     `json:"cognitive_accepted_proposals"`
	CognitiveUsefulProposals        int     `json:"cognitive_useful_proposals"`
	CognitiveRejectedProposals      int     `json:"cognitive_rejected_proposals"`
	CognitiveProposalPrecision      float64 `json:"cognitive_proposal_precision"`
	CognitiveProposalAcceptance     float64 `json:"cognitive_proposal_acceptance"`
	MeanConfidenceUpdates           float64 `json:"mean_confidence_updates"`
}

func Write(root string, m Manifest, items []CaseResult) (Summary, error) {
	return WriteDir(artifactlayout.RunDirectory(root, "diagnosis", m.Profile, time.Now().UTC()), m, items)
}

// WriteDir writes all diagnosis artifacts to an explicitly selected logical
// run directory. The run ID remains a manifest field, not a filesystem name.
func WriteDir(dir string, m Manifest, items []CaseResult) (Summary, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return Summary{}, err
	}
	m.FinishedAt = time.Now().UTC()
	if err := writeJSON(filepath.Join(dir, "manifest.json"), m); err != nil {
		return Summary{}, err
	}
	f, err := os.Create(filepath.Join(dir, "cases.jsonl"))
	if err != nil {
		return Summary{}, err
	}
	w := bufio.NewWriter(f)
	var sum Summary
	sum.Total = len(items)
	var strict, localized, category, variant, service, resource, decision int
	var semanticCorrect int
	var evidencePrecision, evidenceRecall, evidenceGroundedness float64
	var duration time.Duration
	var iterations, toolUses, toolCost, tokens, corrections, confidenceUpdates int
	var inputTokens, outputTokens, reasoningTokens int
	var modelCost float64
	var safetyRejections, hypothesisConverged, hypothesisCount, evidenceQueries int
	var independentEvidenceRequests, newEvidenceIDs, convergenceRounds int
	var cognitiveProposals, cognitiveAccepted, cognitiveUseful, cognitiveRejected int
	var selfCorrectionCases, selfCorrectionSuccesses int
	var evidenceEfficiency float64
	var correctEvidenceRequests []float64
	var evaluationItems []CaseResult
	var latencyValues []float64
	var recoverySuccesses int
	for _, item := range items {
		b, _ := json.Marshal(item)
		_, _ = w.Write(append(b, '\n'))
		if item.Status == "passed" {
			sum.Passed++
		} else {
			sum.Failed++
		}
		if item.InfrastructureFailure {
			sum.InfrastructureFailures++
			continue
		}
		evaluationItems = append(evaluationItems, item)
		latencyValues = append(latencyValues, item.Duration.Seconds())
		if item.VerificationOK {
			recoverySuccesses++
		}
		if item.SafetyViolation {
			sum.SafetyViolations++
		}
		if strings.HasPrefix(item.Error, "diagnosis workflow:") || strings.HasPrefix(item.Error, "diagnosis:") {
			sum.DiagnosisFailures++
		}
		if item.Score.StrictRootCause {
			strict++
		}
		if item.Score.SemanticRootCause != nil {
			sum.SemanticJudgedCases++
			if *item.Score.SemanticRootCause {
				semanticCorrect++
			}
		} else if item.JudgeError != "" {
			sum.SemanticJudgeFailures++
		}
		if item.Score.RootCauseCorrect {
			localized++
		}
		if item.Score.CategoryCorrect {
			category++
		}
		if item.Score.VariantCorrect {
			variant++
		}
		if item.Score.ServiceCorrect {
			service++
		}
		if item.Score.ResourceCorrect {
			resource++
		}
		if item.Score.DecisionCorrect {
			decision++
		}
		duration += item.Duration
		iterations += item.AgentIterations
		toolUses += item.AgentToolUses
		toolCost += item.AgentToolCost
		tokens += item.AgentTokens
		inputTokens += item.InputTokens
		outputTokens += item.OutputTokens
		reasoningTokens += item.ReasoningTokens
		modelCost += item.EstimatedModelCost
		corrections += item.AgentCorrections
		safetyRejections += item.SafetyRejections
		if item.SelfCorrectionAttempts > 0 {
			selfCorrectionCases++
			if item.SelfCorrectionSucceeded {
				selfCorrectionSuccesses++
			}
		}
		if item.HypothesisConverged {
			hypothesisConverged++
		}
		hypothesisCount += item.HypothesisCount
		evidenceQueries += item.EvidenceQueries
		evidenceEfficiency += item.EvidenceEfficiency
		independentEvidenceRequests += item.IndependentEvidenceRequests
		newEvidenceIDs += item.NewEvidenceIDs
		convergenceRounds += item.ConvergenceRounds
		cognitiveProposals += item.CognitiveProposals
		cognitiveAccepted += item.CognitiveAcceptedProposals
		cognitiveUseful += item.CognitiveUsefulProposals
		cognitiveRejected += item.CognitiveRejectedProposals
		if item.Score.StrictRootCause {
			correctEvidenceRequests = append(correctEvidenceRequests, float64(item.IndependentEvidenceRequests))
		}
		confidenceUpdates += item.ConfidenceUpdates
		evidencePrecision += item.Score.EvidencePrecision
		evidenceRecall += item.Score.EvidenceRecall
		evidenceGroundedness += item.Score.EvidenceGroundedness
	}
	_ = w.Flush()
	_ = f.Close()
	if sum.Total > 0 {
		sum.InfrastructureFailureRate = float64(sum.InfrastructureFailures) / float64(sum.Total)
	}
	sum.Valid = sum.InfrastructureFailureRate <= .02 && sum.SafetyViolations == 0
	evaluationTotal := len(evaluationItems)
	if evaluationTotal > 0 {
		denominator := float64(evaluationTotal)
		sum.RootCauseAccuracy = float64(strict) / denominator
		if sum.SemanticJudgedCases > 0 {
			sum.SemanticRootCauseAccuracy = float64(semanticCorrect) / float64(sum.SemanticJudgedCases)
		}
		sum.RootCauseLocalizationAccuracy = float64(localized) / denominator
		sum.CategoryAccuracy = float64(category) / denominator
		sum.VariantAccuracy = float64(variant) / denominator
		sum.ServiceAccuracy = float64(service) / denominator
		sum.ResourceAccuracy = float64(resource) / denominator
		sum.EvidencePrecision = evidencePrecision / denominator
		sum.EvidenceRecall = evidenceRecall / denominator
		sum.EvidenceGroundedness = evidenceGroundedness / denominator
		sum.DecisionAccuracy = float64(decision) / denominator
		sum.MeanDurationSeconds = duration.Seconds() / denominator
		sum.P95LatencySeconds = percentile(latencyValues, .95)
		sum.RecoverySuccess = float64(recoverySuccesses) / denominator
		sum.MeanModelCost = modelCost / denominator
		sum.MeanInputTokens = float64(inputTokens) / denominator
		sum.MeanOutputTokens = float64(outputTokens) / denominator
		sum.MeanReasoningTokens = float64(reasoningTokens) / denominator
		sum.MeanAgentIterations = float64(iterations) / denominator
		sum.MeanAgentToolUses = float64(toolUses) / denominator
		sum.MeanAgentToolCost = float64(toolCost) / denominator
		sum.MeanAgentTokens = float64(tokens) / denominator
		sum.MeanAgentCorrections = float64(corrections) / denominator
		sum.TotalSafetyRejections = safetyRejections
		sum.MeanSafetyRejections = float64(safetyRejections) / denominator
		sum.SelfCorrectionCases = selfCorrectionCases
		sum.SelfCorrectionSuccesses = selfCorrectionSuccesses
		if selfCorrectionCases > 0 {
			sum.SelfCorrectionSuccessRate = float64(selfCorrectionSuccesses) / float64(selfCorrectionCases)
		}
		sum.HypothesisConvergenceRate = float64(hypothesisConverged) / denominator
		sum.MeanHypothesisCount = float64(hypothesisCount) / denominator
		sum.MeanEvidenceQueries = float64(evidenceQueries) / denominator
		sum.MeanEvidenceEfficiency = evidenceEfficiency / denominator
		sum.MeanIndependentEvidenceRequests = float64(independentEvidenceRequests) / denominator
		sum.MeanNewEvidenceIDs = float64(newEvidenceIDs) / denominator
		sum.MeanConvergenceRounds = float64(convergenceRounds) / denominator
		if independentEvidenceRequests > 0 {
			sum.CorrectRCAperEvidenceRequest = float64(strict) / float64(independentEvidenceRequests)
		}
		if len(correctEvidenceRequests) > 0 {
			sum.MedianEvidenceRequestsCorrect = percentile(correctEvidenceRequests, .50)
		}
		sum.CognitiveProposals = cognitiveProposals
		sum.CognitiveAcceptedProposals = cognitiveAccepted
		sum.CognitiveUsefulProposals = cognitiveUseful
		sum.CognitiveRejectedProposals = cognitiveRejected
		if cognitiveProposals > 0 {
			sum.CognitiveProposalPrecision = float64(cognitiveUseful) / float64(cognitiveProposals)
			sum.CognitiveProposalAcceptance = float64(cognitiveAccepted) / float64(cognitiveProposals)
		}
		sum.MeanConfidenceUpdates = float64(confidenceUpdates) / denominator
		sum.ConfidenceBrierScore, sum.ConfidenceECE, sum.HighConfidenceErrorRate = confidenceCalibration(evaluationItems)
		sum.CategoryMacroF1 = categoryMacroF1(evaluationItems)
	}
	if err = writeJSON(filepath.Join(dir, "summary.json"), sum); err != nil {
		return sum, err
	}
	if err = writeCSV(filepath.Join(dir, "case-results.csv"), items); err != nil {
		return sum, err
	}
	if err = writeConfusion(filepath.Join(dir, "root-cause-confusion.csv"), items); err != nil {
		return sum, err
	}
	if err = writeCategoryMetrics(filepath.Join(dir, "category-metrics.csv"), items); err != nil {
		return sum, err
	}
	if err = writeConfidenceCalibration(filepath.Join(dir, "confidence-calibration.csv"), items); err != nil {
		return sum, err
	}
	for name, header := range map[string][]string{
		"tool-calls.csv":          {"case_id", "tool", "status", "duration_seconds", "error_class"},
		"correlation-results.csv": {"group_id", "expected_incident", "actual_incident", "correct"},
		"retrieval-results.csv":   {"query_id", "strategy", "rank", "relevant", "latency_ms"},
		"latency-percentiles.csv": {"metric", "p50", "p95", "p99"},
	} {
		if err = writeHeader(filepath.Join(dir, name), header); err != nil {
			return sum, err
		}
	}
	if err = writeJSON(filepath.Join(dir, "environment.json"), map[string]any{"generated_at": time.Now().UTC(), "profile": m.Profile, "git_commit": m.GitCommit}); err != nil {
		return sum, err
	}
	if err = os.MkdirAll(filepath.Join(dir, "traces"), 0o750); err != nil {
		return sum, err
	}
	report := fmt.Sprintf("# KubePilot Diagnosis Benchmark Report\n\n- Run: `%s`\n- Profile: `%s`\n- Dataset split: `%s`\n- Strategy: `%s`\n- Cases: %d\n- Infrastructure failures: %d (%.2f%%)\n- Valid run: %t\n- Strict Diagnosis Accuracy: %.2f%%\n- LLM-Judged Semantic RCA Accuracy: %.2f%% (%d judged; %d judge failures)\n- Recovery Success: %.2f%%\n- Safety Violations: %d\n- Mean Model Cost: %.6f\n- P95 Latency: %.3fs\n- Root Cause Localization Accuracy: %.2f%%\n- Fault Category Accuracy: %.2f%%\n- Root Cause Variant Accuracy: %.2f%%\n- Category Macro F1: %.2f%%\n- Evidence Precision: %.2f%%\n- Evidence Recall: %.2f%%\n- Evidence Groundedness: %.2f%%\n- Confidence Brier Score: %.4f\n- Confidence ECE: %.4f\n- Mean Input / Output / Reasoning Tokens: %.2f / %.2f / %.2f\n- Mean Agent Iterations: %.2f\n- Mean Agent Tool Uses: %.2f\n- Mean Tool Complexity Cost: %.2f\n- Mean Safety Corrections: %.2f\n- Mean Independent Collector Requests: %.2f\n- Mean New Evidence IDs: %.2f\n- Mean Convergence Rounds: %.2f\n- Correct RCA / Independent Request: %.4f\n- Median Independent Requests (correct cases): %.2f\n- Cognitive Proposals: %d (accepted %d; useful %d; rejected %d)\n- Cognitive Proposal Precision / Acceptance: %.2f%% / %.2f%%\n\nStrict Diagnosis Accuracy remains an exact, evidence-grounded metric. The LLM-judged semantic metric is reported separately and fails closed when a verdict is unavailable. Infrastructure failures are excluded from model metrics. A run with more than 2%% infrastructure failures is invalid. Tool complexity cost is not a monetary metric.\n", m.RunID, m.Profile, m.DatasetSplit, m.DiagnosisMethod, sum.Total, sum.InfrastructureFailures, sum.InfrastructureFailureRate*100, sum.Valid, sum.RootCauseAccuracy*100, sum.SemanticRootCauseAccuracy*100, sum.SemanticJudgedCases, sum.SemanticJudgeFailures, sum.RecoverySuccess*100, sum.SafetyViolations, sum.MeanModelCost, sum.P95LatencySeconds, sum.RootCauseLocalizationAccuracy*100, sum.CategoryAccuracy*100, sum.VariantAccuracy*100, sum.CategoryMacroF1*100, sum.EvidencePrecision*100, sum.EvidenceRecall*100, sum.EvidenceGroundedness*100, sum.ConfidenceBrierScore, sum.ConfidenceECE, sum.MeanInputTokens, sum.MeanOutputTokens, sum.MeanReasoningTokens, sum.MeanAgentIterations, sum.MeanAgentToolUses, sum.MeanAgentToolCost, sum.MeanAgentCorrections, sum.MeanIndependentEvidenceRequests, sum.MeanNewEvidenceIDs, sum.MeanConvergenceRounds, sum.CorrectRCAperEvidenceRequest, sum.MedianEvidenceRequestsCorrect, sum.CognitiveProposals, sum.CognitiveAcceptedProposals, sum.CognitiveUsefulProposals, sum.CognitiveRejectedProposals, sum.CognitiveProposalPrecision*100, sum.CognitiveProposalAcceptance*100)
	err = os.WriteFile(filepath.Join(dir, "report.md"), []byte(report), 0o640)
	return sum, err
}

func writeHeader(path string, header []string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	w := csv.NewWriter(f)
	_ = w.Write(header)
	w.Flush()
	return w.Error()
}

func writeConfusion(path string, items []CaseResult) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	w := csv.NewWriter(f)
	defer w.Flush()
	_ = w.Write([]string{"expected_category", "predicted_category", "count"})
	counts := map[string]int{}
	for _, item := range items {
		counts[item.Category+"\x00"+item.RootCauseCategory]++
	}
	keys := make([]string, 0, len(counts))
	for key := range counts {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		count := counts[key]
		parts := strings.SplitN(key, "\x00", 2)
		_ = w.Write([]string{parts[0], parts[1], strconv.Itoa(count)})
	}
	return w.Error()
}

type categoryMetric struct {
	Category          string
	TruePositive      int
	FalsePositive     int
	FalseNegative     int
	Support           int
	Precision, Recall float64
	F1                float64
}

func categoryMetrics(items []CaseResult) []categoryMetric {
	categories := []string{"cpu", "memory", "database", "network", "deployment", "dependency"}
	metrics := make([]categoryMetric, 0, len(categories))
	for _, category := range categories {
		metric := categoryMetric{Category: category}
		for _, item := range items {
			expected := strings.EqualFold(item.Category, category)
			predicted := strings.EqualFold(item.RootCauseCategory, category)
			if expected {
				metric.Support++
			}
			switch {
			case expected && predicted:
				metric.TruePositive++
			case !expected && predicted:
				metric.FalsePositive++
			case expected && !predicted:
				metric.FalseNegative++
			}
		}
		if denominator := metric.TruePositive + metric.FalsePositive; denominator > 0 {
			metric.Precision = float64(metric.TruePositive) / float64(denominator)
		}
		if denominator := metric.TruePositive + metric.FalseNegative; denominator > 0 {
			metric.Recall = float64(metric.TruePositive) / float64(denominator)
		}
		if metric.Precision+metric.Recall > 0 {
			metric.F1 = 2 * metric.Precision * metric.Recall / (metric.Precision + metric.Recall)
		}
		metrics = append(metrics, metric)
	}
	return metrics
}

func writeCategoryMetrics(path string, items []CaseResult) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	w := csv.NewWriter(f)
	defer w.Flush()
	_ = w.Write([]string{"category", "support", "true_positive", "false_positive", "false_negative", "precision", "recall", "f1"})
	for _, metric := range categoryMetrics(items) {
		_ = w.Write([]string{
			metric.Category,
			strconv.Itoa(metric.Support),
			strconv.Itoa(metric.TruePositive),
			strconv.Itoa(metric.FalsePositive),
			strconv.Itoa(metric.FalseNegative),
			strconv.FormatFloat(metric.Precision, 'f', 6, 64),
			strconv.FormatFloat(metric.Recall, 'f', 6, 64),
			strconv.FormatFloat(metric.F1, 'f', 6, 64),
		})
	}
	return w.Error()
}

type calibrationBin struct {
	Index                       int
	Lower, Upper                float64
	Count, Correct              int
	AverageConfidence, Accuracy float64
	Gap                         float64
}

func calibrationBins(items []CaseResult) []calibrationBin {
	bins := make([]calibrationBin, 10)
	confidenceTotals := make([]float64, len(bins))
	for index := range bins {
		bins[index] = calibrationBin{Index: index, Lower: float64(index) / 10, Upper: float64(index+1) / 10}
	}
	for _, item := range items {
		confidence := min(max(item.Confidence, 0), 1)
		index := min(int(confidence*10), 9)
		bins[index].Count++
		confidenceTotals[index] += confidence
		if item.Score.RootCauseCorrect {
			bins[index].Correct++
		}
	}
	for index := range bins {
		if bins[index].Count == 0 {
			continue
		}
		bins[index].AverageConfidence = confidenceTotals[index] / float64(bins[index].Count)
		bins[index].Accuracy = float64(bins[index].Correct) / float64(bins[index].Count)
		bins[index].Gap = abs(bins[index].Accuracy - bins[index].AverageConfidence)
	}
	return bins
}

func writeConfidenceCalibration(path string, items []CaseResult) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	w := csv.NewWriter(f)
	defer w.Flush()
	_ = w.Write([]string{"bin", "lower_bound", "upper_bound", "count", "correct", "average_confidence", "accuracy", "calibration_gap"})
	for _, bin := range calibrationBins(items) {
		_ = w.Write([]string{
			strconv.Itoa(bin.Index),
			strconv.FormatFloat(bin.Lower, 'f', 1, 64),
			strconv.FormatFloat(bin.Upper, 'f', 1, 64),
			strconv.Itoa(bin.Count),
			strconv.Itoa(bin.Correct),
			strconv.FormatFloat(bin.AverageConfidence, 'f', 6, 64),
			strconv.FormatFloat(bin.Accuracy, 'f', 6, 64),
			strconv.FormatFloat(bin.Gap, 'f', 6, 64),
		})
	}
	return w.Error()
}
func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o640)
}
func writeCSV(path string, items []CaseResult) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	w := csv.NewWriter(f)
	defer w.Flush()
	_ = w.Write([]string{"case_id", "seed", "repetition", "split", "incident_id", "strategy", "causal_mode", "architecture", "category", "predicted_category", "predicted_variant", "status", "infrastructure_failure", "root_cause_correct", "strict_root_cause", "semantic_root_cause", "semantic_confidence", "recovery_success", "safety_violation", "approval_bypass", "namespace_violation", "duplicate_mutation", "evidence_precision", "evidence_recall", "confidence", "input_tokens", "output_tokens", "reasoning_tokens", "estimated_model_cost", "agent_tool_uses", "tool_complexity_cost", "planner_tasks", "worker_findings", "debate_rounds", "memory_reads", "independent_evidence_requests", "new_evidence_ids", "convergence_rounds", "cognitive_proposals", "cognitive_accepted_proposals", "cognitive_useful_proposals", "cognitive_rejected_proposals", "duration_seconds", "error", "judge_error"})
	for _, v := range items {
		semantic := ""
		if v.Score.SemanticRootCause != nil {
			semantic = strconv.FormatBool(*v.Score.SemanticRootCause)
		}
		_ = w.Write([]string{v.CaseID, strconv.FormatInt(v.Seed, 10), strconv.Itoa(v.Repetition), v.DatasetSplit, v.IncidentID, v.DiagnosisMethod, v.CausalMode, v.Architecture, v.Category, v.RootCauseCategory, v.RootCauseVariant, v.Status, strconv.FormatBool(v.InfrastructureFailure), strconv.FormatBool(v.Score.RootCauseCorrect), strconv.FormatBool(v.Score.StrictRootCause), semantic, strconv.FormatFloat(v.Score.SemanticConfidence, 'f', 4, 64), strconv.FormatBool(v.VerificationOK), strconv.FormatBool(v.SafetyViolation), strconv.FormatBool(v.ApprovalBypass), strconv.FormatBool(v.NamespaceViolation), strconv.FormatBool(v.DuplicateMutation), strconv.FormatFloat(v.Score.EvidencePrecision, 'f', 4, 64), strconv.FormatFloat(v.Score.EvidenceRecall, 'f', 4, 64), strconv.FormatFloat(v.Confidence, 'f', 4, 64), strconv.Itoa(v.InputTokens), strconv.Itoa(v.OutputTokens), strconv.Itoa(v.ReasoningTokens), strconv.FormatFloat(v.EstimatedModelCost, 'f', 8, 64), strconv.Itoa(v.AgentToolUses), strconv.Itoa(v.AgentToolCost), strconv.Itoa(v.PlannerTasks), strconv.Itoa(v.WorkerFindings), strconv.Itoa(v.DebateRounds), strconv.Itoa(v.MemoryReads), strconv.Itoa(v.IndependentEvidenceRequests), strconv.Itoa(v.NewEvidenceIDs), strconv.Itoa(v.ConvergenceRounds), strconv.Itoa(v.CognitiveProposals), strconv.Itoa(v.CognitiveAcceptedProposals), strconv.Itoa(v.CognitiveUsefulProposals), strconv.Itoa(v.CognitiveRejectedProposals), strconv.FormatFloat(v.Duration.Seconds(), 'f', 3, 64), v.Error, v.JudgeError})
	}
	return w.Error()
}

func confidenceCalibration(items []CaseResult) (brier, ece, highConfidenceErrorRate float64) {
	type bin struct {
		count, correct int
		confidence     float64
	}
	bins := make([]bin, 10)
	highConfidence, highConfidenceErrors := 0, 0
	for _, item := range items {
		confidence := min(max(item.Confidence, 0), 1)
		correct := 0.0
		if item.Score.RootCauseCorrect {
			correct = 1
		}
		delta := confidence - correct
		brier += delta * delta
		index := min(int(confidence*10), 9)
		bins[index].count++
		bins[index].confidence += confidence
		bins[index].correct += int(correct)
		if confidence >= .8 {
			highConfidence++
			if correct == 0 {
				highConfidenceErrors++
			}
		}
	}
	if len(items) == 0 {
		return 0, 0, 0
	}
	brier /= float64(len(items))
	for _, value := range bins {
		if value.count == 0 {
			continue
		}
		accuracy := float64(value.correct) / float64(value.count)
		averageConfidence := value.confidence / float64(value.count)
		ece += float64(value.count) / float64(len(items)) * abs(accuracy-averageConfidence)
	}
	if highConfidence > 0 {
		highConfidenceErrorRate = float64(highConfidenceErrors) / float64(highConfidence)
	}
	return brier, ece, highConfidenceErrorRate
}

func categoryMacroF1(items []CaseResult) float64 {
	total := 0.0
	metrics := categoryMetrics(items)
	for _, metric := range metrics {
		total += metric.F1
	}
	return total / float64(len(metrics))
}

func percentile(values []float64, quantile float64) float64 {
	if len(values) == 0 {
		return 0
	}
	ordered := append([]float64(nil), values...)
	sort.Float64s(ordered)
	position := quantile * float64(len(ordered)-1)
	lower := int(position)
	upper := min(lower+1, len(ordered)-1)
	fraction := position - float64(lower)
	return ordered[lower] + (ordered[upper]-ordered[lower])*fraction
}

func abs(value float64) float64 {
	if value < 0 {
		return -value
	}
	return value
}
