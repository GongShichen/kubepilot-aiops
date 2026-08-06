package main

import (
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	benchmarkcomparison "github.com/kubepilot-aiops/kubepilot/benchmark/comparison"
	"github.com/kubepilot-aiops/kubepilot/benchmark/reporter"
	"github.com/kubepilot-aiops/kubepilot/internal/domain"
)

func runCausalAblationReport(args []string) {
	fs := flag.NewFlagSet("causal-ablation-report", flag.ExitOnError)
	runID := fs.String("run-id", "", "parent run ID shared by all causal modes")
	output := fs.String("output", "", "exact causal ablation artifact directory")
	paths := map[string]*string{}
	for _, mode := range []string{domain.CausalModeNone, domain.CausalModeStatic, domain.CausalModeLearned, domain.CausalModeFull} {
		paths[mode] = fs.String(mode, "", "exact cases.jsonl for "+mode)
	}
	_ = fs.Parse(args)
	if strings.TrimSpace(*runID) == "" || strings.TrimSpace(*output) == "" {
		fatal(fmt.Errorf("--run-id and --output are required"))
	}
	caseResults := map[string][]reporter.CaseResult{}
	for mode, path := range paths {
		if strings.TrimSpace(*path) == "" {
			fatal(fmt.Errorf("--%s is required", mode))
		}
		var manifest reporter.Manifest
		fatal(readJSON(filepath.Join(filepath.Dir(*path), "manifest.json"), &manifest))
		if manifest.RunID != *runID || manifest.DiagnosisMethod != domain.DiagnosisMethodKubePilot || manifest.CausalMode != mode {
			fatal(fmt.Errorf("%s artifact does not belong to causal ablation run %s", mode, *runID))
		}
		items, err := readCaseResults(*path)
		fatal(err)
		caseResults[mode] = items
	}
	report, err := benchmarkcomparison.BuildCausalAblation(*runID, caseResults)
	fatal(err)
	fatal(os.MkdirAll(*output, 0o750))
	raw, err := json.MarshalIndent(report, "", "  ")
	fatal(err)
	fatal(os.WriteFile(filepath.Join(*output, "causal-ablation.json"), raw, 0o640))
	fatal(writeCausalAblationSystems(filepath.Join(*output, "causal-ablation-systems.csv"), report))
	fatal(writeCausalAblationComparisons(filepath.Join(*output, "causal-ablation-comparisons.csv"), report))
	var markdown strings.Builder
	fmt.Fprintf(&markdown, "# Causal Knowledge Ablation\n\n- Run: `%s`\n- Valid: `%t`\n\n", report.RunID, report.Valid)
	markdown.WriteString("| Mode | Strict Diagnosis Accuracy (95% CI) | Recovery Success (95% CI) | Evidence Efficiency (95% CI) | Safety Violations |\n|---|---:|---:|---:|---:|\n")
	for _, item := range report.Systems {
		fmt.Fprintf(&markdown, "| %s | %.2f%% [%.2f, %.2f] | %.2f%% [%.2f, %.2f] | %.4f [%.4f, %.4f] | %d |\n", item.Mode, item.DiagnosisAccuracy.Estimate*100, item.DiagnosisAccuracy.Lower*100, item.DiagnosisAccuracy.Upper*100, item.RecoverySuccess.Estimate*100, item.RecoverySuccess.Lower*100, item.RecoverySuccess.Upper*100, item.EvidenceEfficiency.Estimate, item.EvidenceEfficiency.Lower, item.EvidenceEfficiency.Upper, item.SafetyViolations)
	}
	markdown.WriteString("\nComparisons are paired by case, seed, and repetition. Binary outcomes use McNemar; evidence efficiency uses Wilcoxon; p-values use Holm correction. Infrastructure failures are excluded and invalidate a mode above 2%; any protected safety violation also invalidates the mode.\n")
	fatal(os.WriteFile(filepath.Join(*output, "report.md"), []byte(markdown.String()), 0o640))
	fmt.Printf("causal ablation run=%s output=%s\n", *runID, *output)
}

func writeCausalAblationSystems(path string, report benchmarkcomparison.CausalAblationReport) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()
	writer := csv.NewWriter(file)
	defer writer.Flush()
	_ = writer.Write([]string{"mode", "cases", "valid", "strict_accuracy", "strict_ci_lower", "strict_ci_upper", "recovery_success", "recovery_ci_lower", "recovery_ci_upper", "evidence_efficiency", "evidence_efficiency_ci_lower", "evidence_efficiency_ci_upper", "safety_violations"})
	for _, item := range report.Systems {
		_ = writer.Write([]string{item.Mode, strconv.Itoa(item.Cases), strconv.FormatBool(item.Valid), formatFloat(item.DiagnosisAccuracy.Estimate), formatFloat(item.DiagnosisAccuracy.Lower), formatFloat(item.DiagnosisAccuracy.Upper), formatFloat(item.RecoverySuccess.Estimate), formatFloat(item.RecoverySuccess.Lower), formatFloat(item.RecoverySuccess.Upper), formatFloat(item.EvidenceEfficiency.Estimate), formatFloat(item.EvidenceEfficiency.Lower), formatFloat(item.EvidenceEfficiency.Upper), strconv.Itoa(item.SafetyViolations)})
	}
	return writer.Error()
}

func writeCausalAblationComparisons(path string, report benchmarkcomparison.CausalAblationReport) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()
	writer := csv.NewWriter(file)
	defer writer.Flush()
	_ = writer.Write([]string{"baseline", "target", "metric", "pairs", "difference", "ci_lower", "ci_upper", "test", "p_value", "holm_adjusted_p_value", "effect_size"})
	for _, item := range report.Comparisons {
		_ = writer.Write([]string{item.Baseline, item.Target, item.Metric, strconv.Itoa(item.Pairs), formatFloat(item.Difference.Estimate), formatFloat(item.Difference.Lower), formatFloat(item.Difference.Upper), item.Test, formatFloat(item.PValue), formatFloat(item.HolmAdjustedPValue), formatFloat(item.EffectSize)})
	}
	return writer.Error()
}
