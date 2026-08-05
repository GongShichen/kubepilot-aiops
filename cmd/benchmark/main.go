package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	artifactlayout "github.com/kubepilot-aiops/kubepilot/benchmark/artifactlayout"
	causalbenchmark "github.com/kubepilot-aiops/kubepilot/benchmark/causal"
	causaldiscoverybenchmark "github.com/kubepilot-aiops/kubepilot/benchmark/causaldiscovery"
	causalevolution "github.com/kubepilot-aiops/kubepilot/benchmark/causalevolution"
	"github.com/kubepilot-aiops/kubepilot/benchmark/correlation"
	"github.com/kubepilot-aiops/kubepilot/benchmark/datasets"
	"github.com/kubepilot-aiops/kubepilot/benchmark/history"
	"github.com/kubepilot-aiops/kubepilot/benchmark/injector"
	benchmarkmanifests "github.com/kubepilot-aiops/kubepilot/benchmark/manifests"
	"github.com/kubepilot-aiops/kubepilot/benchmark/reporter"
	benchmarkreports "github.com/kubepilot-aiops/kubepilot/benchmark/reports"
	"github.com/kubepilot-aiops/kubepilot/benchmark/runner"
	"github.com/kubepilot-aiops/kubepilot/benchmark/scenarios"
	"github.com/kubepilot-aiops/kubepilot/benchmark/scorer"
	topologybenchmark "github.com/kubepilot-aiops/kubepilot/benchmark/topology"
	topologyevolution "github.com/kubepilot-aiops/kubepilot/benchmark/topologyevolution"
	"github.com/kubepilot-aiops/kubepilot/internal/config"
	"github.com/kubepilot-aiops/kubepilot/internal/domain"
	llm "github.com/kubepilot-aiops/kubepilot/internal/model"
	rerankerclient "github.com/kubepilot-aiops/kubepilot/internal/retrieval/reranker"
	"github.com/kubepilot-aiops/kubepilot/retrieval"
	"github.com/oklog/ulid/v2"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "validate":
		validate(os.Args[2:])
	case "environment":
		environment(os.Args[2:])
	case "failure-report":
		failureReport(os.Args[2:])
	case "run":
		run(os.Args[2:])
	case "generate-logs":
		generateLogs(os.Args[2:])
	case "correlation":
		runCorrelation(os.Args[2:])
	case "log-retrieval":
		runLogRetrieval(os.Args[2:])
	case "incident-retrieval":
		runIncidentRetrieval(os.Args[2:])
	case "agent-report":
		runAgentReport(os.Args[2:])
	case "recovery-report":
		runRecoveryReport(os.Args[2:])
	case "autonomous-report":
		runAutonomousReport(os.Args[2:])
	case "suite-report":
		runSuiteReport(os.Args[2:])
	case "intelligence":
		runIntelligence(os.Args[2:])
	case "seed-history":
		seedHistory(os.Args[2:])
	case "resume":
		resume(os.Args[2:])
	case "report":
		report(os.Args[2:])
	default:
		usage()
		os.Exit(2)
	}
}
func usage() {
	fmt.Fprintln(os.Stderr, "kubepilot-benchmark <validate|environment|failure-report|run|resume|report|correlation|log-retrieval|incident-retrieval|agent-report|recovery-report|autonomous-report|suite-report|intelligence|seed-history|generate-logs>")
}

func runIntelligence(args []string) {
	fs := flag.NewFlagSet("intelligence", flag.ExitOnError)
	output := fs.String("output", "", "summary path; defaults to a timestamped knowledge-evolution directory")
	_ = fs.Parse(args)
	if *output == "" {
		*output = filepath.Join(artifactlayout.RunDirectory("artifacts/benchmark", "knowledge-evolution", "full", time.Now().UTC()), "summary.json")
	}
	topologyScore := topologybenchmark.Evaluate(topologybenchmark.DefaultCases())
	causalScore := causalbenchmark.Evaluate(nil, causalbenchmark.DefaultCases())
	topologyEvolutionScore := topologyevolution.Evaluate(topologyevolution.DefaultCases())
	causalEvolutionScore := causalevolution.Evaluate(causalevolution.DefaultCases())
	causalDiscoveryScore := causaldiscoverybenchmark.Evaluate(causaldiscoverybenchmark.DefaultCases())
	payload := map[string]any{"topology": topologyScore, "causal": causalScore, "topology_evolution": topologyEvolutionScore, "causal_evolution": causalEvolutionScore, "causal_discovery": causalDiscoveryScore, "mode": "offline-evaluator"}
	raw, err := json.MarshalIndent(payload, "", "  ")
	fatal(err)
	fatal(os.MkdirAll(filepath.Dir(*output), 0o750))
	fatal(os.WriteFile(*output, raw, 0o640))
	fmt.Printf("topology_recall_at_1=%.2f%% causal_root_cause_accuracy=%.2f%% topology_pattern_recall=%.2f%% causal_evolution_accuracy=%.2f%% causal_discovery_recall=%.2f%% output=%s\n", topologyScore.RecallAt1*100, causalScore.RootCauseAccuracy*100, topologyEvolutionScore.PatternRecall*100, causalEvolutionScore.CausalAccuracy*100, causalDiscoveryScore.PatternRecall*100, *output)
}
func validate(args []string) {
	fs := flag.NewFlagSet("validate", flag.ExitOnError)
	catalog := fs.String("catalog", "benchmark/incidents.yaml", "scenario catalog")
	_ = fs.Parse(args)
	_, items, hash, err := scenarios.Load(*catalog)
	fatal(err)
	fmt.Printf("valid: %d scenarios, catalog_sha256=%s\n", len(items), hash)
}

func environment(args []string) {
	fs := flag.NewFlagSet("environment", flag.ExitOnError)
	manifestPath := fs.String("manifest", "benchmark/manifests/autonomous.yaml", "benchmark manifest")
	output := fs.String("output", "benchmark/reports/runtime_manifest.json", "redacted runtime manifest")
	_ = fs.Parse(args)
	base, _, err := benchmarkmanifests.Load(*manifestPath)
	fatal(err)
	commit := gitCommit()
	fatal(benchmarkmanifests.WriteRuntime(*output, benchmarkmanifests.RuntimeFromEnv(base, commit, time.Now().UTC())))
	fmt.Printf("runtime manifest written: %s\n", *output)
}

func failureReport(args []string) {
	fs := flag.NewFlagSet("failure-report", flag.ExitOnError)
	output := fs.String("output", "benchmark/reports/failure.json", "failure report path")
	phase := fs.String("phase", "", "benchmark phase")
	category := fs.String("category", "", "failure category")
	reason := fs.String("reason", "", "redacted reason")
	impact := fs.String("impact", "", "impact")
	_ = fs.Parse(args)
	fatal(benchmarkreports.WriteFailure(*output, *phase, *category, *reason, *impact, time.Now().UTC()))
	fmt.Printf("failure report written: %s\n", *output)
}
func run(args []string) {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	catalog := fs.String("catalog", "benchmark/incidents.yaml", "scenario catalog")
	profile := fs.String("profile", "smoke", "smoke, ci, standard or robustness")
	agentURL := fs.String("agent-url", "http://localhost:8080", "agent URL")
	token := fs.String("token", os.Getenv("API_TOKEN"), "agent token")
	kubeconfig := fs.String("kubeconfig", os.Getenv("KUBECONFIG"), "kubeconfig path")
	artifactRoot := fs.String("artifacts", "artifacts/benchmark", "artifact root")
	artifactDir := fs.String("artifact-dir", "", "logical output directory; defaults to artifacts/<suite>/<profile>/<timestamp>")
	dryRun := fs.Bool("dry-run-injector", false, "validate runner without changing Kubernetes")
	autoApprove := fs.Bool("auto-approve", false, "approve only ground-truth-safe proposals")
	caseID := fs.String("case-id", "", "run exactly one scenario ID (diagnostic use)")
	runID := fs.String("run-id", "", "stable run ID used for resume/API orchestration")
	resumeRun := fs.Bool("resume", false, "continue after the last checkpoint")
	diagnosisMethod := fs.String("diagnosis-method", domain.DiagnosisMethodKubePilot, "llm-only, vector-rag, or kubepilot")
	compareMethods := fs.Bool("compare-methods", false, "run all diagnosis baselines sequentially")
	_ = fs.Parse(args)
	runCtx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	if !domain.ValidDiagnosisMethod(*diagnosisMethod) || *diagnosisMethod == "" {
		fatal(fmt.Errorf("unsupported diagnosis method %q", *diagnosisMethod))
	}
	if *artifactDir == "" && *resumeRun && *runID != "" {
		*artifactDir = findRunDirectory(*artifactRoot, *runID)
	}
	if *artifactDir == "" {
		*artifactDir = artifactlayout.RunDirectory(*artifactRoot, "diagnosis", *profile, time.Now().UTC())
	}
	if *compareMethods {
		if *profile != "smoke" && *profile != "ci" && *profile != "standard" {
			fatal(fmt.Errorf("--compare-methods supports smoke, ci, or standard profiles"))
		}
		if *runID == "" {
			*runID = ulid.Make().String()
		}
		comparisonDir := artifactlayout.RunDirectory(*artifactRoot, "diagnosis", *profile, time.Now().UTC())
		methods := []string{domain.DiagnosisMethodLLMOnly, domain.DiagnosisMethodVectorRAG, domain.DiagnosisMethodKubePilot}
		for _, method := range methods {
			run([]string{"--profile", *profile, "--run-id", *runID + "-" + method, "--catalog", *catalog, "--agent-url", *agentURL, "--token", *token, "--kubeconfig", *kubeconfig, "--artifacts", *artifactRoot, "--artifact-dir", filepath.Join(comparisonDir, method), "--auto-approve=" + strconv.FormatBool(*autoApprove), "--dry-run-injector=" + strconv.FormatBool(*dryRun), "--diagnosis-method", method})
			if runCtx.Err() != nil {
				fmt.Fprintln(os.Stderr, "benchmark interrupted after cleaning the active case")
				return
			}
		}
		fatal(writeDiagnosisComparison(comparisonDir, *profile, *runID, methods))
		fmt.Printf("diagnosis comparison artifacts=%s\n", comparisonDir)
		return
	}
	if *profile == "correlation" {
		correlationDir := artifactlayout.RunDirectory(*artifactRoot, "correlation", "full", time.Now().UTC())
		runCorrelation([]string{"--output", filepath.Join(correlationDir, "correlation-summary.json")})
		return
	}
	if *profile == "full" {
		if *runID == "" {
			*runID = ulid.Make().String()
		}
		fullDir := artifactlayout.RunDirectory(*artifactRoot, "autonomous", "full", time.Now().UTC())
		standardArgs := []string{"--profile", "standard", "--run-id", *runID, "--catalog", *catalog, "--agent-url", *agentURL, "--token", *token, "--kubeconfig", *kubeconfig, "--artifacts", *artifactRoot, "--artifact-dir", filepath.Join(fullDir, "diagnosis"), "--auto-approve=" + strconv.FormatBool(*autoApprove)}
		run(standardArgs)
		runCorrelation([]string{"--output", filepath.Join(fullDir, "correlation", "correlation-summary.json")})
		return
	}
	_, items, hash, err := scenarios.Load(*catalog)
	fatal(err)
	if *profile == "smoke" {
		items = smoke(items)
	} else if *profile == "ci" {
		items = append([]scenarios.Scenario(nil), items[:min(10, len(items))]...)
	} else if *profile == "robustness" {
		items = robustness(items)
	} else if *profile != "standard" {
		fatal(fmt.Errorf("unsupported profile %q", *profile))
	}
	if *caseID != "" {
		var selected []scenarios.Scenario
		for _, item := range items {
			if item.ID == *caseID {
				selected = append(selected, item)
				break
			}
		}
		if len(selected) == 0 {
			fatal(fmt.Errorf("scenario %q not found in profile %s", *caseID, *profile))
		}
		items = selected
	}
	reg := injector.NewRegistry()
	var inj injector.Injector
	if *dryRun {
		inj = &injector.DryRun{}
	} else {
		if *kubeconfig == "" {
			fatal(fmt.Errorf("--kubeconfig is required unless --dry-run-injector is used"))
		}
		inj, err = injector.NewKubernetes(*kubeconfig)
		fatal(err)
	}
	for _, name := range []string{"service_fault", "resource_patch", "traffic", "dependency_scale", "config_patch", "network_policy", "service_patch", "deployment_patch"} {
		reg.Register(name, inj)
	}
	if *runID == "" {
		*runID = ulid.Make().String()
	}
	checkpoint := filepath.Join(*artifactDir, "checkpoint.jsonl")
	chatEndpoint := strings.TrimRight(os.Getenv("CHAT_BASE_URL"), "/") + "/" + strings.TrimLeft(env("CHAT_API_PATH", "/chat/completions"), "/")
	endpointHash := sha256.Sum256([]byte(chatEndpoint))
	historyHash, _ := fileSHA256("benchmark/history.yaml")
	sourceHash, _ := sourceTreeSHA256(".")
	modelConfigHash := diagnosisModelConfigHash()
	skillHash, err := diagnosisSkillSnapshotHash()
	fatal(err)
	rankingHash, err := fileSHA256(env("RANKING_POLICY_FILE", "knowledge/ranking_policy.yaml"))
	fatal(err)
	toolCostHash, err := fileSHA256(env("TOOL_COST_FILE", "internal/agent/skills/tool_costs.yaml"))
	fatal(err)
	rerankerModel, rerankerHash := diagnosisRerankerIdentity()
	manifestHash, manifestErr := fileSHA256("benchmark/manifests/default.yaml")
	fatal(manifestErr)
	manifest := reporter.Manifest{ManifestHash: manifestHash, RunID: *runID, Profile: *profile, CatalogHash: hash, Protocol: env("CHAT_PROTOCOL", "openai-compatible"), Model: os.Getenv("CHAT_MODEL"), EndpointHash: hex.EncodeToString(endpointHash[:]), ModelConfigHash: modelConfigHash, SkillSnapshotHash: skillHash, RankingPolicyHash: rankingHash, ToolCostPolicyHash: toolCostHash, BudgetConfigHash: diagnosisBudgetConfigHash(), RerankerModel: rerankerModel, RerankerConfigHash: rerankerHash, EmbeddingModel: os.Getenv("EMBEDDING_MODEL"), EmbeddingDimensions: env("EMBEDDING_DIMENSIONS", "1024"), DiagnosisMethod: *diagnosisMethod, GitCommit: gitCommit(), SourceHash: sourceHash, HistoryDatasetHash: historyHash, HistoryCollection: env("HISTORY_COLLECTION", "kubepilot_history"), Seed: 20260803, StartedAt: time.Now().UTC()}
	var previous []reporter.CaseResult
	if *resumeRun {
		var existing reporter.Manifest
		fatal(readJSON(filepath.Join(*artifactDir, "manifest.json"), &existing))
		fatal(validateResumeManifest(existing, manifest))
		manifest.StartedAt = existing.StartedAt
		loaded, loadErr := readCaseResults(checkpoint)
		err = loadErr
		fatal(err)
		completed := map[string]bool{}
		for _, result := range loaded {
			if result.Status == "cleanup_failed" || strings.Contains(strings.ToLower(result.Error), "context canceled") {
				continue
			}
			previous = append(previous, result)
			completed[result.CaseID] = true
		}
		pending := items[:0]
		for _, item := range items {
			if !completed[item.ID] {
				pending = append(pending, item)
			}
		}
		items = pending
	} else {
		if _, statErr := os.Stat(checkpoint); statErr == nil {
			fatal(fmt.Errorf("run %q already has a checkpoint; use resume or a new run ID", *runID))
		} else if !os.IsNotExist(statErr) {
			fatal(statErr)
		}
		fatal(reporter.WriteManifestDir(*artifactDir, manifest))
	}
	client := runner.NewHTTPClient(*agentURL, *token)
	client.DiagnosisMethod = *diagnosisMethod
	r := &runner.Runner{Registry: reg, Client: client, AutoApprove: *autoApprove, PollInterval: time.Second, MaxCaseRestarts: 1, DiagnosisTimeout: benchmarkDiagnosisTimeout(), CaseTimeout: benchmarkCaseTimeout(), DiagnosisMethod: *diagnosisMethod, OnResult: func(result reporter.CaseResult) { fatal(appendCheckpoint(checkpoint, result)) }}
	results := append(previous, r.Run(runCtx, items)...)
	summary, err := reporter.WriteDir(*artifactDir, manifest, results)
	fatal(err)
	fmt.Printf("run=%s total=%d passed=%d root_cause_accuracy=%.2f%% artifacts=%s\n", manifest.RunID, summary.Total, summary.Passed, summary.RootCauseAccuracy*100, *artifactDir)
	if failedCase, ok := cleanupFailure(results); ok {
		fatal(fmt.Errorf("benchmark stopped after cleanup failure in case %s", failedCase))
	}
}

func cleanupFailure(results []reporter.CaseResult) (string, bool) {
	for _, result := range results {
		if result.Status == "cleanup_failed" {
			return result.CaseID, true
		}
	}
	return "", false
}

func diagnosisModelConfigHash() string {
	configuration := map[string]string{
		"protocol": env("CHAT_PROTOCOL", "openai-compatible"), "endpoint": strings.TrimRight(os.Getenv("CHAT_BASE_URL"), "/") + "/" + strings.TrimLeft(env("CHAT_API_PATH", "/chat/completions"), "/"),
		"model": os.Getenv("CHAT_MODEL"), "timeout": env("CHAT_TIMEOUT", "60s"), "max_tokens": env("CHAT_MAX_TOKENS", "4096"),
		"temperature": env("CHAT_TEMPERATURE", "0"), "reasoning_effort": os.Getenv("CHAT_REASONING_EFFORT"), "max_retries": env("CHAT_MAX_RETRIES", "3"),
	}
	encoded, _ := json.Marshal(configuration)
	hash := sha256.Sum256(encoded)
	return hex.EncodeToString(hash[:])
}

func diagnosisSkillSnapshotHash() (string, error) {
	files := []struct{ agent, path string }{
		{"diagnosis_agent", "internal/agent/skills/diagnosis/SKILL.md"},
		{"recovery_agent", "internal/agent/skills/recovery/SKILL.md"},
		{"supervisor_agent", "internal/agent/skills/supervisor/SKILL.md"},
	}
	h := sha256.New()
	for _, item := range files {
		fileHash, err := fileSHA256(item.path)
		if err != nil {
			return "", err
		}
		_, _ = h.Write([]byte(item.agent + ":" + fileHash + "\n"))
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func diagnosisBudgetConfigHash() string {
	configuration := map[string]string{}
	for key, fallback := range map[string]string{
		"SUPERVISOR_MAX_ITERATIONS": "10", "SUPERVISOR_MAX_TOOL_USES": "8", "SUPERVISOR_MAX_TOOL_COST": "24", "SUPERVISOR_MAX_TOKENS": "12000", "SUPERVISOR_MAX_CORRECTIONS": "3",
		"DIAGNOSIS_MAX_ITERATIONS": "12", "DIAGNOSIS_MAX_TOOL_USES": "24", "DIAGNOSIS_MAX_TOOL_COST": "48", "DIAGNOSIS_MAX_TOKENS": "30000", "DIAGNOSIS_MAX_CORRECTIONS": "3",
		"RECOVERY_MAX_ITERATIONS": "10", "RECOVERY_MAX_TOOL_USES": "10", "RECOVERY_MAX_TOOL_COST": "16", "RECOVERY_MAX_TOKENS": "16000", "RECOVERY_MAX_CORRECTIONS": "2",
		"INCIDENT_MAX_AGENT_TOOL_USES": "30", "INCIDENT_MAX_AGENT_TOOL_COST": "72", "INCIDENT_MAX_TOKENS": "58000",
	} {
		configuration[key] = env(key, fallback)
	}
	encoded, _ := json.Marshal(configuration)
	hash := sha256.Sum256(encoded)
	return hex.EncodeToString(hash[:])
}

func diagnosisRerankerIdentity() (string, string) {
	if !strings.EqualFold(env("RERANKER_ENABLED", "false"), "true") {
		return "", ""
	}
	cfg := config.RerankerConfig{Enabled: true, Protocol: env("RERANKER_PROTOCOL", "openai-compatible"), BaseURL: os.Getenv("RERANKER_BASE_URL"), APIPath: env("RERANKER_API_PATH", "/reranks"), Model: os.Getenv("RERANKER_MODEL")}
	return cfg.Model, rerankerclient.New(cfg).ConfigHash()
}

func validateResumeManifest(existing, current reporter.Manifest) error {
	type field struct{ name, old, new string }
	fields := []field{
		{"profile", existing.Profile, current.Profile}, {"catalog_hash", existing.CatalogHash, current.CatalogHash},
		{"manifest_hash", existing.ManifestHash, current.ManifestHash},
		{"chat_protocol", existing.Protocol, current.Protocol}, {"chat_model", existing.Model, current.Model},
		{"endpoint_hash", existing.EndpointHash, current.EndpointHash}, {"model_config_hash", existing.ModelConfigHash, current.ModelConfigHash},
		{"skill_snapshot_hash", existing.SkillSnapshotHash, current.SkillSnapshotHash}, {"ranking_policy_hash", existing.RankingPolicyHash, current.RankingPolicyHash},
		{"tool_cost_policy_hash", existing.ToolCostPolicyHash, current.ToolCostPolicyHash}, {"budget_config_hash", existing.BudgetConfigHash, current.BudgetConfigHash},
		{"reranker_model", existing.RerankerModel, current.RerankerModel}, {"reranker_config_hash", existing.RerankerConfigHash, current.RerankerConfigHash},
		{"embedding_model", existing.EmbeddingModel, current.EmbeddingModel}, {"embedding_dimensions", existing.EmbeddingDimensions, current.EmbeddingDimensions},
		{"diagnosis_method", existing.DiagnosisMethod, current.DiagnosisMethod}, {"git_commit", existing.GitCommit, current.GitCommit},
		{"source_hash", existing.SourceHash, current.SourceHash}, {"history_dataset_hash", existing.HistoryDatasetHash, current.HistoryDatasetHash},
		{"history_collection", existing.HistoryCollection, current.HistoryCollection},
	}
	for _, item := range fields {
		if item.old != item.new {
			return fmt.Errorf("cannot resume run with changed %s (recorded=%q current=%q); start a new run ID", item.name, item.old, item.new)
		}
	}
	return nil
}

func writeDiagnosisComparison(root, profile, runID string, methods []string) error {
	type row struct {
		Method  string           `json:"method"`
		Summary reporter.Summary `json:"summary"`
	}
	rows := make([]row, 0, len(methods))
	for _, method := range methods {
		var summary reporter.Summary
		if err := readJSON(filepath.Join(root, method, "summary.json"), &summary); err != nil {
			return err
		}
		rows = append(rows, row{Method: method, Summary: summary})
	}
	if err := os.MkdirAll(root, 0o750); err != nil {
		return err
	}
	b, err := json.MarshalIndent(map[string]any{"run_id": runID, "profile": profile, "methods": rows}, "", "  ")
	if err != nil {
		return err
	}
	if err = os.WriteFile(filepath.Join(root, "diagnosis-comparison.json"), b, 0o640); err != nil {
		return err
	}
	var report strings.Builder
	fmt.Fprintf(&report, "# KubePilot Diagnosis Baseline Comparison\n\n- Run: `%s`\n- Profile: `%s`\n\n", runID, profile)
	report.WriteString("| Method | Strict RCA | Localization | Category | Variant | Evidence Precision | Evidence Recall | Brier | ECE |\n|---|---:|---:|---:|---:|---:|---:|---:|---:|\n")
	for _, item := range rows {
		s := item.Summary
		fmt.Fprintf(&report, "| %s | %.2f%% | %.2f%% | %.2f%% | %.2f%% | %.2f%% | %.2f%% | %.4f | %.4f |\n", item.Method, s.RootCauseAccuracy*100, s.RootCauseLocalizationAccuracy*100, s.CategoryAccuracy*100, s.VariantAccuracy*100, s.EvidencePrecision*100, s.EvidenceRecall*100, s.ConfidenceBrierScore, s.ConfidenceECE)
	}
	report.WriteString("\nLocalization requires exact category, root-cause variant, service, and resource matching. Strict RCA additionally requires at least 50% required-evidence recall.\n")
	return os.WriteFile(filepath.Join(root, "report.md"), []byte(report.String()), 0o640)
}
func robustness(all []scenarios.Scenario) []scenarios.Scenario {
	counts := map[string]int{}
	var selected []scenarios.Scenario
	for _, scenario := range all {
		if counts[scenario.Category] < 6 {
			selected = append(selected, scenario)
			counts[scenario.Category]++
		}
	}
	var out []scenarios.Scenario
	for repeat := 1; repeat <= 3; repeat++ {
		for _, scenario := range selected {
			copy := scenario
			copy.ID = fmt.Sprintf("%s-run-%d", scenario.ID, repeat)
			out = append(out, copy)
		}
	}
	return out
}

func gitCommit() string {
	b, err := exec.Command("git", "rev-parse", "--verify", "HEAD").Output()
	if err != nil || strings.TrimSpace(string(b)) == "" {
		return "uncommitted"
	}
	return strings.TrimSpace(string(b))
}

func sourceTreeSHA256(root string) (string, error) {
	h := sha256.New()
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if entry.IsDir() {
			switch rel {
			case ".git", "artifacts", ".idea", ".vscode":
				return filepath.SkipDir
			}
			return nil
		}
		base, ext := filepath.Base(path), filepath.Ext(path)
		allowed := ext == ".go" || ext == ".yaml" || ext == ".yml" || ext == ".py" || ext == ".sql"
		allowed = allowed || base == "go.mod" || base == "go.sum" || base == "Dockerfile" || base == "Makefile"
		if !allowed {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		_, _ = h.Write([]byte(filepath.ToSlash(rel)))
		_, _ = h.Write([]byte{0})
		_, _ = h.Write(b)
		_, _ = h.Write([]byte{0})
		return nil
	})
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
func generateLogs(args []string) {
	fs := flag.NewFlagSet("generate-logs", flag.ExitOnError)
	output := fs.String("output", "", "output JSONL; defaults to a timestamped log-retrieval dataset directory")
	count := fs.Int("count", 500000, "record count")
	seed := fs.Uint64("seed", 20260803, "seed")
	_ = fs.Parse(args)
	if *output == "" {
		*output = filepath.Join(artifactlayout.RunDirectory("artifacts/benchmark", "log-retrieval", "dataset", time.Now().UTC()), "logs.jsonl")
	}
	fatal(os.MkdirAll(filepath.Dir(*output), 0o750))
	fatal(datasets.GenerateLogs(*output, *count, *seed))
	fmt.Printf("generated %d records at %s\n", *count, *output)
}

func runCorrelation(args []string) {
	fs := flag.NewFlagSet("correlation", flag.ExitOnError)
	output := fs.String("output", "", "summary path; defaults to a timestamped correlation directory")
	groups := fs.Int("groups", 100, "ground-truth groups")
	seed := fs.Uint64("seed", 20260803, "seed")
	agentURL := fs.String("agent-url", "", "optional live Agent URL")
	webhookToken := fs.String("webhook-token", os.Getenv("ALERTMANAGER_WEBHOOK_TOKEN"), "Alertmanager webhook token")
	runSalt := fs.String("run-salt", ulid.Make().String(), "unique live-correlation run identifier")
	_ = fs.Parse(args)
	if *output == "" {
		*output = filepath.Join(artifactlayout.RunDirectory("artifacts/benchmark", "correlation", "full", time.Now().UTC()), "correlation-summary.json")
	}
	items := correlation.Generate(*groups, 2, 8, *seed)
	actual := correlation.Correlate(items)
	if *agentURL != "" {
		var err error
		actual, err = liveCorrelation(context.Background(), *agentURL, *webhookToken, *runSalt, items)
		fatal(err)
	}
	score := scorer.Correlation(correlation.Expected(items), actual)
	fatal(os.MkdirAll(filepath.Dir(*output), 0o750))
	b, _ := json.MarshalIndent(map[string]any{"groups": *groups, "alerts": len(items), "seed": *seed, "run_salt": *runSalt, "score": score}, "", "  ")
	fatal(os.WriteFile(*output, b, 0o640))
	fmt.Printf("groups=%d alerts=%d exact=%.2f%% pairwise_f1=%.2f%% output=%s\n", *groups, len(items), score.ExactAccuracy*100, score.F1*100, *output)
}

func liveCorrelation(ctx context.Context, base, token, runSalt string, items []correlation.Alert) (map[string]string, error) {
	out := map[string]string{}
	client := &http.Client{Timeout: 15 * time.Second}
	for _, item := range items {
		payload := map[string]any{"status": "firing", "alerts": []any{map[string]any{"status": "firing", "fingerprint": runSalt + "-" + item.Fingerprint, "startsAt": item.StartsAt, "labels": map[string]string{"alertname": "BenchmarkAlert", "namespace": item.Namespace, "service": item.Service, "deployment": item.Service, "trace_id": runSalt + "-" + item.TraceID, "revision": runSalt + "-" + item.Revision, "severity": "warning", "benchmark_mode": "correlation", "correlation_run": runSalt}, "annotations": map[string]string{"summary": "correlation benchmark"}}}}
		b, _ := json.Marshal(payload)
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(base, "/")+"/api/v1/alerts/alertmanager", bytes.NewReader(b))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		if resp.StatusCode/100 != 2 {
			return nil, fmt.Errorf("alert webhook status %d: %s", resp.StatusCode, string(raw))
		}
		var result struct {
			IncidentIDs []string `json:"incident_ids"`
		}
		if err = json.Unmarshal(raw, &result); err != nil || len(result.IncidentIDs) == 0 {
			return nil, fmt.Errorf("invalid alert webhook response")
		}
		out[item.ID] = result.IncidentIDs[0]
	}
	return out, nil
}

func seedHistory(args []string) {
	fs := flag.NewFlagSet("seed-history", flag.ExitOnError)
	dataset := fs.String("dataset", "benchmark/history.yaml", "held-out historical incident dataset")
	milvusURL := fs.String("milvus-url", env("MILVUS_ADDRESS", "localhost:19530"), "Milvus address")
	collection := fs.String("collection", env("HISTORY_COLLECTION", "kubepilot_history"), "isolated history collection")
	dimensions := fs.Int("dimensions", envInt("EMBEDDING_DIMENSIONS", 1024), "embedding dimensions")
	batchSize := fs.Int("embedding-batch-size", envInt("EMBEDDING_BATCH_SIZE", 10), "maximum texts per embedding request")
	requestInterval := fs.Duration("embedding-request-interval", envDuration("EMBEDDING_REQUEST_INTERVAL", time.Second), "minimum interval between embedding requests")
	concurrency := fs.Int("embedding-concurrency", envInt("EMBEDDING_CONCURRENCY", 1), "maximum concurrent embedding requests")
	output := fs.String("output", "", "seed manifest; defaults to a timestamped history dataset directory")
	_ = fs.Parse(args)
	if *output == "" {
		*output = filepath.Join(artifactlayout.RunDirectory("artifacts/benchmark", "history-seed", "full", time.Now().UTC()), "manifest.json")
	}
	_, items, datasetHash, err := history.Load(*dataset)
	fatal(err)
	embedCfg := config.EmbeddingConfig{
		BaseURL: os.Getenv("EMBEDDING_BASE_URL"), APIPath: env("EMBEDDING_API_PATH", "/embeddings"),
		APIKey: os.Getenv("EMBEDDING_API_KEY"), Model: os.Getenv("EMBEDDING_MODEL"),
		Dimensions: *dimensions, BatchSize: *batchSize, Concurrency: *concurrency, Timeout: 30 * time.Second, RequestInterval: *requestInterval, MaxRetries: envInt("EMBEDDING_MAX_RETRIES", 3),
	}
	if embedCfg.BaseURL == "" || embedCfg.APIKey == "" || embedCfg.Model == "" {
		fatal(fmt.Errorf("embedding configuration is required to seed history"))
	}
	docs := make([]retrieval.Document, len(items))
	texts := make([]string, len(items))
	for i, item := range items {
		docs[i] = item.Document
		texts[i] = item.Text
	}
	embedder := llm.NewEmbedder(embedCfg)
	var vectors [][]float32
	calls := 0
	for start := 0; start < len(texts); start += *batchSize {
		end := min(start+*batchSize, len(texts))
		batch, embedErr := embedder.Embed(context.Background(), texts[start:end])
		fatal(embedErr)
		vectors = append(vectors, batch...)
		calls++
	}
	for i := range docs {
		docs[i].Vector = vectors[i]
	}
	store := retrieval.NewMilvusStore(*milvusURL, *collection, *dimensions)
	fatal(store.Ensure(context.Background()))
	for start := 0; start < len(docs); start += 100 {
		fatal(store.Upsert(context.Background(), docs[start:min(start+100, len(docs))]))
	}
	endpointHash := sha256.Sum256([]byte(embedCfg.BaseURL))
	manifest := map[string]any{
		"documents": len(docs), "embedding_calls": calls, "dataset_sha256": datasetHash,
		"collection":      *collection,
		"embedding_model": embedCfg.Model, "embedding_dimensions": *dimensions,
		"embedding_endpoint_hash": hex.EncodeToString(endpointHash[:]), "generated_at": time.Now().UTC(),
	}
	b, err := json.MarshalIndent(manifest, "", "  ")
	fatal(err)
	fatal(os.MkdirAll(filepath.Dir(*output), 0o750))
	fatal(os.WriteFile(*output, b, 0o640))
	fmt.Printf("seeded history documents=%d embedding_calls=%d output=%s\n", len(docs), calls, *output)
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err = io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func resume(args []string) {
	fs := flag.NewFlagSet("resume", flag.ExitOnError)
	runID := fs.String("run-id", "", "run ID")
	root := fs.String("artifacts", "artifacts/benchmark", "artifact root")
	artifactDir := fs.String("artifact-dir", "", "logical diagnosis artifact directory")
	agentURL := fs.String("agent-url", "http://localhost:8080", "agent URL")
	token := fs.String("token", os.Getenv("API_TOKEN"), "agent token")
	kubeconfig := fs.String("kubeconfig", os.Getenv("KUBECONFIG"), "kubeconfig")
	auto := fs.Bool("auto-approve", false, "safe auto approval")
	_ = fs.Parse(args)
	if *runID == "" {
		fatal(fmt.Errorf("--run-id is required"))
	}
	if *artifactDir == "" {
		*artifactDir = findRunDirectory(*root, *runID)
		if *artifactDir == "" {
			*artifactDir = filepath.Join(*root, *runID) // legacy layout
		}
	}
	var manifest reporter.Manifest
	fatal(readJSON(filepath.Join(*artifactDir, "manifest.json"), &manifest))
	method := manifest.DiagnosisMethod
	if method == "" {
		method = domain.DiagnosisMethodKubePilot
	}
	run([]string{"--profile", manifest.Profile, "--run-id", *runID, "--artifacts", *root, "--artifact-dir", *artifactDir, "--agent-url", *agentURL, "--token", *token, "--kubeconfig", *kubeconfig, "--resume=true", "--auto-approve=" + strconv.FormatBool(*auto), "--diagnosis-method", method})
}

func report(args []string) {
	fs := flag.NewFlagSet("report", flag.ExitOnError)
	runID := fs.String("run-id", "", "run ID")
	root := fs.String("artifacts", "artifacts/benchmark", "artifact root")
	artifactDir := fs.String("artifact-dir", "", "logical diagnosis artifact directory")
	_ = fs.Parse(args)
	if *runID == "" {
		fatal(fmt.Errorf("--run-id is required"))
	}
	dir := *artifactDir
	if dir == "" {
		dir = findRunDirectory(*root, *runID)
		if dir == "" {
			dir = filepath.Join(*root, *runID) // legacy layout
		}
	}
	var manifest reporter.Manifest
	fatal(readJSON(filepath.Join(dir, "manifest.json"), &manifest))
	items, err := readCaseResults(filepath.Join(dir, "cases.jsonl"))
	fatal(err)
	_, err = reporter.WriteDir(dir, manifest, items)
	fatal(err)
	fmt.Println(filepath.Join(dir, "report.md"))
}

func appendCheckpoint(path string, result reporter.CaseResult) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o640)
	if err != nil {
		return err
	}
	defer f.Close()
	return json.NewEncoder(f).Encode(result)
}
func readCaseResults(path string) ([]reporter.CaseResult, error) {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	var out []reporter.CaseResult
	for scanner.Scan() {
		var value reporter.CaseResult
		if err = json.Unmarshal(scanner.Bytes(), &value); err != nil {
			return nil, err
		}
		out = append(out, value)
	}
	return out, scanner.Err()
}
func readJSON(path string, out any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, out)
}

func findRunDirectory(root, runID string) string {
	if strings.TrimSpace(runID) == "" {
		return ""
	}
	var found string
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || filepath.Base(path) != "manifest.json" {
			return nil
		}
		var manifest reporter.Manifest
		if err := readJSON(path, &manifest); err == nil && manifest.RunID == runID {
			found = filepath.Dir(path)
		}
		return nil
	})
	return found
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
func envInt(key string, fallback int) int {
	value, err := strconv.Atoi(os.Getenv(key))
	if err == nil && value > 0 {
		return value
	}
	return fallback
}

func benchmarkDiagnosisTimeout() time.Duration {
	requestTimeout, err := time.ParseDuration(env("CHAT_TIMEOUT", "120s"))
	if err != nil || requestTimeout <= 0 {
		requestTimeout = 120 * time.Second
	}
	retries := envInt("CHAT_MAX_RETRIES", 3)
	if retries < 3 {
		retries = 3
	}
	// Leave enough room for the initial request and every configured retry.
	// The lower bound also covers normal multi-tool Agent turns without making
	// successful cases wait longer than their actual completion time.
	window := requestTimeout * time.Duration(retries+1)
	if window < 10*time.Minute {
		window = 10 * time.Minute
	}
	return window + time.Minute
}

func benchmarkCaseTimeout() time.Duration {
	return benchmarkDiagnosisTimeout() + 5*time.Minute
}
func envDuration(key string, fallback time.Duration) time.Duration {
	value, err := time.ParseDuration(os.Getenv(key))
	if err == nil && value >= 0 {
		return value
	}
	return fallback
}
func smoke(all []scenarios.Scenario) []scenarios.Scenario {
	seen := map[string]bool{}
	var out []scenarios.Scenario
	for _, s := range all {
		if !seen[s.Category] {
			out = append(out, s)
			seen[s.Category] = true
		}
	}
	return out
}
func fatal(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", strings.TrimSpace(err.Error()))
		os.Exit(1)
	}
}
