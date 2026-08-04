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
)

type Manifest struct {
	ManifestHash        string    `json:"manifest_hash,omitempty"`
	RunID               string    `json:"run_id"`
	Profile             string    `json:"profile"`
	CatalogHash         string    `json:"catalog_hash"`
	Protocol            string    `json:"chat_protocol"`
	Model               string    `json:"chat_model"`
	EndpointHash        string    `json:"endpoint_hash"`
	ModelConfigHash     string    `json:"model_config_hash"`
	SkillSnapshotHash   string    `json:"skill_snapshot_hash,omitempty"`
	RankingPolicyHash   string    `json:"ranking_policy_hash,omitempty"`
	ToolCostPolicyHash  string    `json:"tool_cost_policy_hash,omitempty"`
	BudgetConfigHash    string    `json:"budget_config_hash,omitempty"`
	RerankerModel       string    `json:"reranker_model,omitempty"`
	RerankerConfigHash  string    `json:"reranker_config_hash,omitempty"`
	EmbeddingModel      string    `json:"embedding_model,omitempty"`
	EmbeddingDimensions string    `json:"embedding_dimensions,omitempty"`
	DiagnosisMethod     string    `json:"diagnosis_method,omitempty"`
	GitCommit           string    `json:"git_commit"`
	SourceHash          string    `json:"source_hash"`
	HistoryDatasetHash  string    `json:"history_dataset_hash,omitempty"`
	HistoryCollection   string    `json:"history_collection,omitempty"`
	Seed                int64     `json:"seed"`
	StartedAt           time.Time `json:"started_at"`
	FinishedAt          time.Time `json:"finished_at,omitempty"`
}

func WriteManifest(root string, manifest Manifest) error {
	dir := filepath.Join(root, manifest.RunID)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	return writeJSON(filepath.Join(dir, "manifest.json"), manifest)
}

type CaseResult struct {
	CaseID                  string        `json:"case_id"`
	IncidentID              string        `json:"incident_id,omitempty"`
	Category                string        `json:"category"`
	Status                  string        `json:"status"`
	Score                   scorer.Score  `json:"score"`
	Duration                time.Duration `json:"duration"`
	Error                   string        `json:"error,omitempty"`
	RootCauseCategory       string        `json:"root_cause_category,omitempty"`
	RootCauseVariant        string        `json:"root_cause_variant,omitempty"`
	Service                 string        `json:"service,omitempty"`
	Resource                string        `json:"resource,omitempty"`
	Confidence              float64       `json:"confidence"`
	DiagnosisMethod         string        `json:"diagnosis_method,omitempty"`
	AgentToolUses           int           `json:"agent_tool_uses"`
	AgentToolCost           int           `json:"agent_tool_cost"`
	AgentTokens             int           `json:"agent_tokens"`
	AgentCorrections        int           `json:"agent_corrections"`
	SafetyRejections        int           `json:"safety_rejections"`
	SelfCorrectionAttempts  int           `json:"self_correction_attempts"`
	SelfCorrectionSucceeded bool          `json:"self_correction_succeeded"`
	HypothesisCount         int           `json:"hypothesis_count"`
	HypothesisConverged     bool          `json:"hypothesis_converged"`
	EvidenceQueries         int           `json:"evidence_queries"`
	EvidenceEfficiency      float64       `json:"evidence_efficiency"`
	ConfidenceUpdates       int           `json:"confidence_updates"`
	AttributedEvidence      int           `json:"attributed_evidence"`
	TopologyCandidates      int           `json:"topology_candidates"`
}
type Summary struct {
	Total                         int     `json:"total"`
	Passed                        int     `json:"passed"`
	Failed                        int     `json:"failed"`
	DiagnosisFailures             int     `json:"diagnosis_failures"`
	RootCauseAccuracy             float64 `json:"root_cause_accuracy"`
	RootCauseLocalizationAccuracy float64 `json:"root_cause_localization_accuracy"`
	CategoryAccuracy              float64 `json:"category_accuracy"`
	VariantAccuracy               float64 `json:"variant_accuracy"`
	ServiceAccuracy               float64 `json:"service_accuracy"`
	ResourceAccuracy              float64 `json:"resource_accuracy"`
	EvidencePrecision             float64 `json:"evidence_precision"`
	EvidenceRecall                float64 `json:"evidence_recall"`
	EvidenceGroundedness          float64 `json:"evidence_groundedness"`
	DecisionAccuracy              float64 `json:"decision_accuracy"`
	ConfidenceBrierScore          float64 `json:"confidence_brier_score"`
	ConfidenceECE                 float64 `json:"confidence_ece"`
	HighConfidenceErrorRate       float64 `json:"high_confidence_error_rate"`
	CategoryMacroF1               float64 `json:"category_macro_f1"`
	MeanDurationSeconds           float64 `json:"mean_duration_seconds"`
	MeanAgentToolUses             float64 `json:"mean_agent_tool_uses"`
	MeanAgentToolCost             float64 `json:"mean_agent_tool_cost"`
	MeanAgentTokens               float64 `json:"mean_agent_tokens"`
	MeanAgentCorrections          float64 `json:"mean_agent_corrections"`
	TotalSafetyRejections         int     `json:"total_safety_rejections"`
	MeanSafetyRejections          float64 `json:"mean_safety_rejections"`
	SelfCorrectionCases           int     `json:"self_correction_cases"`
	SelfCorrectionSuccesses       int     `json:"self_correction_successes"`
	SelfCorrectionSuccessRate     float64 `json:"self_correction_success_rate"`
	HypothesisConvergenceRate     float64 `json:"hypothesis_convergence_rate"`
	MeanHypothesisCount           float64 `json:"mean_hypothesis_count"`
	MeanEvidenceQueries           float64 `json:"mean_evidence_queries"`
	MeanEvidenceEfficiency        float64 `json:"mean_evidence_efficiency"`
	MeanConfidenceUpdates         float64 `json:"mean_confidence_updates"`
}

func Write(root string, m Manifest, items []CaseResult) (Summary, error) {
	dir := filepath.Join(root, m.RunID)
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
	var evidencePrecision, evidenceRecall, evidenceGroundedness float64
	var duration time.Duration
	var toolUses, toolCost, tokens, corrections, confidenceUpdates int
	var safetyRejections, hypothesisConverged, hypothesisCount, evidenceQueries int
	var selfCorrectionCases, selfCorrectionSuccesses int
	var evidenceEfficiency float64
	for _, item := range items {
		b, _ := json.Marshal(item)
		_, _ = w.Write(append(b, '\n'))
		if item.Status == "passed" {
			sum.Passed++
		} else {
			sum.Failed++
		}
		if strings.HasPrefix(item.Error, "diagnosis workflow:") || strings.HasPrefix(item.Error, "diagnosis:") {
			sum.DiagnosisFailures++
		}
		if item.Score.StrictRootCause {
			strict++
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
		toolUses += item.AgentToolUses
		toolCost += item.AgentToolCost
		tokens += item.AgentTokens
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
		confidenceUpdates += item.ConfidenceUpdates
		evidencePrecision += item.Score.EvidencePrecision
		evidenceRecall += item.Score.EvidenceRecall
		evidenceGroundedness += item.Score.EvidenceGroundedness
	}
	_ = w.Flush()
	_ = f.Close()
	if sum.Total > 0 {
		sum.RootCauseAccuracy = float64(strict) / float64(sum.Total)
		sum.RootCauseLocalizationAccuracy = float64(localized) / float64(sum.Total)
		sum.CategoryAccuracy = float64(category) / float64(sum.Total)
		sum.VariantAccuracy = float64(variant) / float64(sum.Total)
		sum.ServiceAccuracy = float64(service) / float64(sum.Total)
		sum.ResourceAccuracy = float64(resource) / float64(sum.Total)
		sum.EvidencePrecision = evidencePrecision / float64(sum.Total)
		sum.EvidenceRecall = evidenceRecall / float64(sum.Total)
		sum.EvidenceGroundedness = evidenceGroundedness / float64(sum.Total)
		sum.DecisionAccuracy = float64(decision) / float64(sum.Total)
		sum.MeanDurationSeconds = duration.Seconds() / float64(sum.Total)
		sum.MeanAgentToolUses = float64(toolUses) / float64(sum.Total)
		sum.MeanAgentToolCost = float64(toolCost) / float64(sum.Total)
		sum.MeanAgentTokens = float64(tokens) / float64(sum.Total)
		sum.MeanAgentCorrections = float64(corrections) / float64(sum.Total)
		sum.TotalSafetyRejections = safetyRejections
		sum.MeanSafetyRejections = float64(safetyRejections) / float64(sum.Total)
		sum.SelfCorrectionCases = selfCorrectionCases
		sum.SelfCorrectionSuccesses = selfCorrectionSuccesses
		if selfCorrectionCases > 0 {
			sum.SelfCorrectionSuccessRate = float64(selfCorrectionSuccesses) / float64(selfCorrectionCases)
		}
		sum.HypothesisConvergenceRate = float64(hypothesisConverged) / float64(sum.Total)
		sum.MeanHypothesisCount = float64(hypothesisCount) / float64(sum.Total)
		sum.MeanEvidenceQueries = float64(evidenceQueries) / float64(sum.Total)
		sum.MeanEvidenceEfficiency = evidenceEfficiency / float64(sum.Total)
		sum.MeanConfidenceUpdates = float64(confidenceUpdates) / float64(sum.Total)
		sum.ConfidenceBrierScore, sum.ConfidenceECE, sum.HighConfidenceErrorRate = confidenceCalibration(items)
		sum.CategoryMacroF1 = categoryMacroF1(items)
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
	report := fmt.Sprintf("# KubePilot Diagnosis Benchmark Report\n\n- Run: `%s`\n- Profile: `%s`\n- Diagnosis method: `%s`\n- Cases: %d\n- Passed: %d\n- Failed: %d\n- Diagnosis workflow failures: %d\n- Strict Root Cause Accuracy: %.2f%%\n- Root Cause Localization Accuracy: %.2f%%\n- Fault Category Accuracy: %.2f%%\n- Root Cause Variant Accuracy: %.2f%%\n- Category Macro F1: %.2f%%\n- Evidence Precision: %.2f%%\n- Evidence Recall: %.2f%%\n- Evidence Groundedness: %.2f%%\n- Confidence Brier Score: %.4f\n- Confidence ECE: %.4f\n- High-confidence Error Rate: %.2f%%\n- Recovery Decision Accuracy: %.2f%%\n- Mean Agent Tool Uses: %.2f\n- Mean Agent Tool Cost: %.2f\n- Mean Agent Tokens: %.2f\n- Mean Safety Corrections: %.2f\n- Total Safety Rejections: %d\n- Self-correction Success Rate: %.2f%%\n- Hypothesis Convergence Rate: %.2f%%\n- Mean Hypothesis Count: %.2f\n- Mean Evidence Queries: %.2f\n- Mean Evidence Efficiency: %.4f\n- Mean Confidence Updates: %.2f\n- Mean Duration: %.3fs\n\nRoot cause localization requires an exact category, variant, service, and resource match. Strict root cause accuracy additionally requires at least 50%% required-evidence recall. Workflow failures remain in the end-to-end denominator and are reported separately. All values in this report are measured from this run.\n", m.RunID, m.Profile, m.DiagnosisMethod, sum.Total, sum.Passed, sum.Failed, sum.DiagnosisFailures, sum.RootCauseAccuracy*100, sum.RootCauseLocalizationAccuracy*100, sum.CategoryAccuracy*100, sum.VariantAccuracy*100, sum.CategoryMacroF1*100, sum.EvidencePrecision*100, sum.EvidenceRecall*100, sum.EvidenceGroundedness*100, sum.ConfidenceBrierScore, sum.ConfidenceECE, sum.HighConfidenceErrorRate*100, sum.DecisionAccuracy*100, sum.MeanAgentToolUses, sum.MeanAgentToolCost, sum.MeanAgentTokens, sum.MeanAgentCorrections, sum.TotalSafetyRejections, sum.SelfCorrectionSuccessRate*100, sum.HypothesisConvergenceRate*100, sum.MeanHypothesisCount, sum.MeanEvidenceQueries, sum.MeanEvidenceEfficiency, sum.MeanConfidenceUpdates, sum.MeanDurationSeconds)
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
	categories := []string{"cpu", "memory", "database", "network", "deployment"}
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
	_ = w.Write([]string{"case_id", "incident_id", "diagnosis_method", "category", "predicted_category", "predicted_variant", "status", "root_cause_correct", "strict_root_cause", "category_correct", "variant_correct", "service_correct", "resource_correct", "decision_correct", "evidence_precision", "evidence_recall", "evidence_groundedness", "confidence", "agent_tool_uses", "agent_tool_cost", "agent_tokens", "agent_corrections", "safety_rejections", "self_correction_attempts", "self_correction_succeeded", "hypothesis_count", "hypothesis_converged", "evidence_queries", "evidence_efficiency", "confidence_updates", "duration_seconds", "error"})
	for _, v := range items {
		_ = w.Write([]string{v.CaseID, v.IncidentID, v.DiagnosisMethod, v.Category, v.RootCauseCategory, v.RootCauseVariant, v.Status, strconv.FormatBool(v.Score.RootCauseCorrect), strconv.FormatBool(v.Score.StrictRootCause), strconv.FormatBool(v.Score.CategoryCorrect), strconv.FormatBool(v.Score.VariantCorrect), strconv.FormatBool(v.Score.ServiceCorrect), strconv.FormatBool(v.Score.ResourceCorrect), strconv.FormatBool(v.Score.DecisionCorrect), strconv.FormatFloat(v.Score.EvidencePrecision, 'f', 4, 64), strconv.FormatFloat(v.Score.EvidenceRecall, 'f', 4, 64), strconv.FormatFloat(v.Score.EvidenceGroundedness, 'f', 4, 64), strconv.FormatFloat(v.Confidence, 'f', 4, 64), strconv.Itoa(v.AgentToolUses), strconv.Itoa(v.AgentToolCost), strconv.Itoa(v.AgentTokens), strconv.Itoa(v.AgentCorrections), strconv.Itoa(v.SafetyRejections), strconv.Itoa(v.SelfCorrectionAttempts), strconv.FormatBool(v.SelfCorrectionSucceeded), strconv.Itoa(v.HypothesisCount), strconv.FormatBool(v.HypothesisConverged), strconv.Itoa(v.EvidenceQueries), strconv.FormatFloat(v.EvidenceEfficiency, 'f', 4, 64), strconv.Itoa(v.ConfidenceUpdates), strconv.FormatFloat(v.Duration.Seconds(), 'f', 3, 64), v.Error})
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

func abs(value float64) float64 {
	if value < 0 {
		return -value
	}
	return value
}
