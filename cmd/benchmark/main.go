package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/csv"
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
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/kubepilot-aiops/kubepilot/agent"
	causalbenchmark "github.com/kubepilot-aiops/kubepilot/benchmark/causal"
	causaldiscoverybenchmark "github.com/kubepilot-aiops/kubepilot/benchmark/causaldiscovery"
	causalevolution "github.com/kubepilot-aiops/kubepilot/benchmark/causalevolution"
	benchmarkcomparison "github.com/kubepilot-aiops/kubepilot/benchmark/comparison"
	"github.com/kubepilot-aiops/kubepilot/benchmark/correlation"
	"github.com/kubepilot-aiops/kubepilot/benchmark/datasets"
	benchmarkenvironment "github.com/kubepilot-aiops/kubepilot/benchmark/environment"
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
	artifactlayout "github.com/kubepilot-aiops/kubepilot/internal/artifacts"
	"github.com/kubepilot-aiops/kubepilot/internal/config"
	"github.com/kubepilot-aiops/kubepilot/internal/domain"
	"github.com/kubepilot-aiops/kubepilot/internal/evaluation"
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
	case "causal-ablation-report":
		runCausalAblationReport(os.Args[2:])
	default:
		usage()
		os.Exit(2)
	}
}
func usage() {
	fmt.Fprintln(os.Stderr, "kubepilot-benchmark <validate|environment|failure-report|run|resume|report|causal-ablation-report|correlation|log-retrieval|incident-retrieval|agent-report|recovery-report|autonomous-report|suite-report|intelligence|seed-history|generate-logs>")
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
	manifestPath := fs.String("manifest", "benchmark/manifests/default.yaml", "benchmark manifest")
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
	diagnosisMethod := fs.String("diagnosis-method", domain.DiagnosisMethodKubePilot, "direct, rag, react, rule-only, evidence-only, cognitive, active-diagnosis, kubepilot, kubepilot-no-reflection, or kubepilot-no-optional-skills")
	causalMode := fs.String("causal-mode", domain.CausalModeFull, "no-causal, static-causal, learned-causal, or full")
	modelProfile := fs.String("model-profile", os.Getenv("MODEL_PROFILE"), "stable label for the active model configuration")
	compareMethods := fs.Bool("compare-methods", false, "run all diagnosis baselines sequentially")
	strategyList := fs.String("strategies", "direct,rag,react,rule-only,evidence-only,cognitive,active-diagnosis,kubepilot,kubepilot-no-reflection,kubepilot-no-optional-skills", "comma-separated strategies used by comparison runs")
	datasetSplit := fs.String("dataset-split", "test", "dev, validation, test, or all")
	seedList := fs.String("seeds", "20260803,20260804,20260805", "comma-separated paired load and fault seeds")
	repetitions := fs.Int("repetitions", 1, "repetitions per scenario and seed")
	workers := fs.Int("workers", envInt("BENCHMARK_WORKERS", 1), "isolated namespace workers")
	modelConcurrency := fs.Int("model-concurrency", envInt("BENCHMARK_MODEL_CONCURRENCY", 0), "maximum cases concurrently using the model; defaults to workers")
	workerNamespaceList := fs.String("worker-namespaces", os.Getenv("BENCHMARK_WORKER_NAMESPACES"), "comma-separated explicit benchmark worker namespace pool")
	semanticJudgeEnabled := fs.Bool("semantic-judge", envBool("BENCHMARK_SEMANTIC_JUDGE", false), "report a separate LLM-judged semantic RCA metric")
	_ = fs.Parse(args)
	runCtx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	canonicalMethod, validMethod := domain.NormalizeDiagnosisMethod(*diagnosisMethod)
	if !validMethod || *diagnosisMethod == "" {
		fatal(fmt.Errorf("unsupported diagnosis method %q", *diagnosisMethod))
	}
	*diagnosisMethod = canonicalMethod
	canonicalCausalMode, validCausalMode := domain.NormalizeCausalMode(*causalMode)
	if !validCausalMode {
		fatal(fmt.Errorf("unsupported causal mode %q", *causalMode))
	}
	*causalMode = canonicalCausalMode
	seeds, seedErr := parseSeeds(*seedList)
	fatal(seedErr)
	if *repetitions < 1 {
		fatal(fmt.Errorf("repetitions must be positive"))
	}
	if *workers < 1 || *workers > 32 {
		fatal(fmt.Errorf("workers must be between 1 and 32"))
	}
	if *modelConcurrency == 0 {
		*modelConcurrency = *workers
	}
	if *modelConcurrency < 1 || *modelConcurrency > *workers {
		fatal(fmt.Errorf("model concurrency must be between 1 and workers"))
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
		comparisonDir := *artifactDir
		methods, strategyErr := parseStrategies(*strategyList)
		fatal(strategyErr)
		if !*resumeRun {
			methods = randomizedStrategyOrder(methods, *runID)
		}
		for _, method := range methods {
			run([]string{"--profile", *profile, "--run-id", *runID, "--catalog", *catalog, "--agent-url", *agentURL, "--token", *token, "--kubeconfig", *kubeconfig, "--artifacts", *artifactRoot, "--artifact-dir", filepath.Join(comparisonDir, method), "--auto-approve=" + strconv.FormatBool(*autoApprove), "--dry-run-injector=" + strconv.FormatBool(*dryRun), "--resume=" + strconv.FormatBool(*resumeRun), "--semantic-judge=" + strconv.FormatBool(*semanticJudgeEnabled), "--diagnosis-method", method, "--causal-mode", *causalMode, "--model-profile", *modelProfile, "--dataset-split", *datasetSplit, "--seeds", *seedList, "--repetitions", strconv.Itoa(*repetitions), "--workers", strconv.Itoa(*workers), "--model-concurrency", strconv.Itoa(*modelConcurrency), "--worker-namespaces", *workerNamespaceList})
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
		runCorrelation([]string{"--output", filepath.Join(correlationDir, "correlation-summary.json"), "--agent-url", *agentURL, "--webhook-token", env("ALERTMANAGER_WEBHOOK_TOKEN", "")})
		return
	}
	if *profile == "full" {
		if *runID == "" {
			*runID = ulid.Make().String()
		}
		fullDir := artifactlayout.RunDirectory(*artifactRoot, "autonomous", "full", time.Now().UTC())
		standardArgs := []string{"--profile", "standard", "--run-id", *runID, "--catalog", *catalog, "--agent-url", *agentURL, "--token", *token, "--kubeconfig", *kubeconfig, "--artifacts", *artifactRoot, "--artifact-dir", filepath.Join(fullDir, "diagnosis"), "--auto-approve=" + strconv.FormatBool(*autoApprove), "--dry-run-injector=" + strconv.FormatBool(*dryRun), "--resume=" + strconv.FormatBool(*resumeRun), "--semantic-judge=" + strconv.FormatBool(*semanticJudgeEnabled), "--diagnosis-method", *diagnosisMethod, "--causal-mode", *causalMode, "--model-profile", *modelProfile, "--dataset-split", *datasetSplit, "--seeds", *seedList, "--repetitions", strconv.Itoa(*repetitions), "--workers", strconv.Itoa(*workers), "--model-concurrency", strconv.Itoa(*modelConcurrency), "--worker-namespaces", *workerNamespaceList}
		run(standardArgs)
		runCorrelation([]string{"--output", filepath.Join(fullDir, "correlation", "correlation-summary.json")})
		return
	}
	catalogConfig, items, hash, err := scenarios.Load(*catalog)
	fatal(err)
	items, err = selectDatasetSplit(items, *datasetSplit)
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
	items = expandScenarioSeeds(items, seeds, *repetitions)
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
	allItems := append([]scenarios.Scenario(nil), items...)
	workerCount := min(*workers, len(allItems))
	effectiveModelConcurrency := min(*modelConcurrency, workerCount)
	workerNamespaces, err := resolveWorkerNamespaces(catalogConfig.Namespace, workerCount, *workerNamespaceList)
	fatal(err)
	if !*dryRun && *kubeconfig == "" {
		fatal(fmt.Errorf("--kubeconfig is required unless --dry-run-injector is used"))
	}
	if !*dryRun && workerCount > 1 {
		provisioner, provisionErr := benchmarkenvironment.NewProvisioner(*kubeconfig, catalogConfig.Namespace)
		fatal(provisionErr)
		prepareCtx, prepareCancel := context.WithTimeout(runCtx, 5*time.Minute)
		provisionErr = provisioner.Prepare(prepareCtx, workerNamespaces)
		prepareCancel()
		fatal(provisionErr)
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
	var judgeConfig config.ChatConfig
	if *semanticJudgeEnabled {
		loadedConfig, configErr := config.Load()
		fatal(configErr)
		judgeConfig, configErr = semanticJudgeChatConfig(loadedConfig.Chat)
		fatal(configErr)
	}
	skillHash, err := diagnosisSkillSnapshotHash(*diagnosisMethod)
	fatal(err)
	rankingHash, err := fileSHA256(env("RANKING_POLICY_FILE", "knowledge/ranking_policy.yaml"))
	fatal(err)
	toolCostHash, err := fileSHA256(env("TOOL_COST_FILE", "internal/agent/skills/tool_costs.yaml"))
	fatal(err)
	rerankerModel, rerankerHash := diagnosisRerankerIdentity()
	manifestHash, manifestErr := fileSHA256("benchmark/manifests/default.yaml")
	fatal(manifestErr)
	manifest := reporter.Manifest{ManifestHash: manifestHash, RunID: *runID, Profile: *profile, CatalogHash: hash, Protocol: env("CHAT_PROTOCOL", "openai-compatible"), Model: os.Getenv("CHAT_MODEL"), ModelProfile: *modelProfile, SemanticJudge: *semanticJudgeEnabled, SemanticJudgeModel: judgeConfig.Model, SemanticJudgeConfig: semanticJudgeConfigHash(judgeConfig), EndpointHash: hex.EncodeToString(endpointHash[:]), ModelConfigHash: modelConfigHash, SkillSnapshotHash: skillHash, RankingPolicyHash: rankingHash, ToolCostPolicyHash: toolCostHash, BudgetConfigHash: diagnosisBudgetConfigHash(), RerankerModel: rerankerModel, RerankerConfigHash: rerankerHash, EmbeddingModel: os.Getenv("EMBEDDING_MODEL"), EmbeddingDimensions: env("EMBEDDING_DIMENSIONS", "1024"), DiagnosisMethod: *diagnosisMethod, CausalMode: *causalMode, Strategies: []string{*diagnosisMethod}, DatasetSplit: *datasetSplit, Seeds: seeds, Repetitions: *repetitions, Architecture: strategyArchitecture(*diagnosisMethod), Parallelism: workerCount, ModelConcurrency: effectiveModelConcurrency, WorkerNamespaces: workerNamespaces, ShardPolicy: runner.StableShardPolicy, PricingSnapshot: pricingSnapshot(), GitCommit: gitCommit(), SourceHash: sourceHash, HistoryDatasetHash: historyHash, HistoryCollection: env("HISTORY_COLLECTION", "kubepilot_history"), Seed: seeds[0], StartedAt: time.Now().UTC()}
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
			completed[caseCheckpointKey(result.CaseID, result.Seed, result.Repetition)] = true
		}
		pending := items[:0]
		for _, item := range items {
			if !completed[caseCheckpointKey(item.ID, item.Seed, item.Repetition)] {
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
	client.CausalMode = *causalMode
	var semanticJudge evaluation.RootCauseJudge
	if *semanticJudgeEnabled {
		chat, chatErr := llm.NewEinoChatModel(runCtx, judgeConfig)
		fatal(chatErr)
		semanticJudge = evaluation.ChatRootCauseJudge{Chat: chat}
	}
	preflightCtx, preflightCancel := context.WithTimeout(runCtx, 3*time.Minute)
	fatal(client.Preflight(preflightCtx))
	preflightCancel()
	gate, err := runner.NewConcurrencyGate(effectiveModelConcurrency)
	fatal(err)
	executionCtx, cancelExecution := context.WithCancel(runCtx)
	defer cancelExecution()
	recorder := &checkpointRecorder{path: checkpoint, cancel: cancelExecution}
	parallelWorkers := make([]runner.ParallelWorker, workerCount)
	for workerIndex, namespace := range workerNamespaces {
		reg := injector.NewRegistry()
		var inj injector.Injector
		if *dryRun {
			inj = &injector.DryRun{}
		} else {
			inj, err = injector.NewKubernetes(*kubeconfig, namespace)
			fatal(err)
		}
		for _, name := range benchmarkInjectorNames() {
			reg.Register(name, inj)
		}
		workerID := fmt.Sprintf("worker-%02d", workerIndex+1)
		workerRunner := &runner.Runner{Registry: reg, Client: client, AutoApprove: *autoApprove, PollInterval: time.Second, MaxCaseRestarts: 1, DiagnosisMethod: *diagnosisMethod, CausalMode: *causalMode, WorkerID: workerID, Gate: gate, SemanticJudge: semanticJudge, OnResult: recorder.Record}
		parallelWorkers[workerIndex] = runner.ParallelWorker{ID: workerID, Namespace: namespace, Runner: workerRunner}
	}
	current, runErr := (runner.ParallelRunner{Workers: parallelWorkers}).Run(executionCtx, items)
	if checkpointErr := recorder.Err(); checkpointErr != nil {
		fatal(checkpointErr)
	}
	results := orderCaseResults(allItems, append(previous, current...))
	summary, err := reporter.WriteDir(*artifactDir, manifest, results)
	fatal(err)
	fmt.Printf("run=%s total=%d passed=%d root_cause_accuracy=%.2f%% artifacts=%s\n", manifest.RunID, summary.Total, summary.Passed, summary.RootCauseAccuracy*100, *artifactDir)
	if runErr != nil {
		fatal(runErr)
	}
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

func benchmarkInjectorNames() []string {
	return []string{"service_fault", "resource_patch", "traffic", "dependency_scale", "config_patch", "network_policy", "service_patch", "deployment_patch"}
}

func resolveWorkerNamespaces(base string, workers int, configured string) ([]string, error) {
	if workers < 1 {
		return nil, fmt.Errorf("at least one worker is required")
	}
	if workers == 1 {
		return []string{base}, nil
	}
	expected, err := benchmarkenvironment.WorkerNamespaces(base, workers)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(configured) == "" {
		return expected, nil
	}
	parts := strings.Split(configured, ",")
	if len(parts) < workers {
		return nil, fmt.Errorf("worker namespace pool has %d entries, need %d", len(parts), workers)
	}
	resolved := make([]string, workers)
	for index := range resolved {
		resolved[index] = strings.TrimSpace(parts[index])
		if resolved[index] != expected[index] {
			return nil, fmt.Errorf("worker namespace %d must be %q, got %q", index+1, expected[index], resolved[index])
		}
	}
	return resolved, nil
}

func orderCaseResults(items []scenarios.Scenario, results []reporter.CaseResult) []reporter.CaseResult {
	order := make(map[string]int, len(items))
	for index, item := range items {
		order[caseCheckpointKey(item.ID, item.Seed, item.Repetition)] = index
	}
	out := append([]reporter.CaseResult(nil), results...)
	sort.SliceStable(out, func(i, j int) bool {
		left, leftOK := order[caseCheckpointKey(out[i].CaseID, out[i].Seed, out[i].Repetition)]
		right, rightOK := order[caseCheckpointKey(out[j].CaseID, out[j].Seed, out[j].Repetition)]
		if !leftOK || !rightOK {
			return caseCheckpointKey(out[i].CaseID, out[i].Seed, out[i].Repetition) < caseCheckpointKey(out[j].CaseID, out[j].Seed, out[j].Repetition)
		}
		return left < right
	})
	return out
}

func diagnosisModelConfigHash() string {
	configuration := map[string]string{
		"protocol": env("CHAT_PROTOCOL", "openai-compatible"), "endpoint": strings.TrimRight(os.Getenv("CHAT_BASE_URL"), "/") + "/" + strings.TrimLeft(env("CHAT_API_PATH", "/chat/completions"), "/"),
		"model": os.Getenv("CHAT_MODEL"), "timeout": env("CHAT_TIMEOUT", "60s"), "max_tokens": env("CHAT_MAX_TOKENS", "8192"),
		"temperature": env("CHAT_TEMPERATURE", "0"), "reasoning_effort": os.Getenv("CHAT_REASONING_EFFORT"), "max_retries": env("CHAT_MAX_RETRIES", "3"), "concurrency": env("CHAT_CONCURRENCY", "4"),
	}
	encoded, _ := json.Marshal(configuration)
	hash := sha256.Sum256(encoded)
	return hex.EncodeToString(hash[:])
}

// semanticJudgeChatConfig starts from CHAT_* so evaluation is comparable by
// default, then permits an explicitly configured independent judge model.
func semanticJudgeChatConfig(base config.ChatConfig) (config.ChatConfig, error) {
	judge := base
	for key, destination := range map[string]*string{
		"JUDGE_CHAT_PROTOCOL":         &judge.Protocol,
		"JUDGE_CHAT_BASE_URL":         &judge.BaseURL,
		"JUDGE_CHAT_API_PATH":         &judge.APIPath,
		"JUDGE_CHAT_API_KEY":          &judge.APIKey,
		"JUDGE_CHAT_MODEL":            &judge.Model,
		"JUDGE_CHAT_REASONING_EFFORT": &judge.ReasoningEffort,
	} {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			*destination = value
		}
	}
	if value := strings.TrimSpace(os.Getenv("JUDGE_CHAT_TIMEOUT")); value != "" {
		parsed, err := time.ParseDuration(value)
		if err != nil || parsed <= 0 {
			return config.ChatConfig{}, fmt.Errorf("JUDGE_CHAT_TIMEOUT must be a positive duration")
		}
		judge.Timeout = parsed
	}
	if value := strings.TrimSpace(os.Getenv("JUDGE_CHAT_MAX_TOKENS")); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed <= 0 {
			return config.ChatConfig{}, fmt.Errorf("JUDGE_CHAT_MAX_TOKENS must be positive")
		}
		judge.MaxTokens = parsed
	}
	if value := strings.TrimSpace(os.Getenv("JUDGE_CHAT_TEMPERATURE")); value != "" {
		parsed, err := strconv.ParseFloat(value, 64)
		if err != nil || parsed < 0 || parsed > 2 {
			return config.ChatConfig{}, fmt.Errorf("JUDGE_CHAT_TEMPERATURE must be between 0 and 2")
		}
		judge.Temperature = parsed
	}
	if value := strings.TrimSpace(os.Getenv("JUDGE_CHAT_MAX_RETRIES")); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 0 || parsed > 3 {
			return config.ChatConfig{}, fmt.Errorf("JUDGE_CHAT_MAX_RETRIES must be between 0 and 3")
		}
		judge.MaxRetries = parsed
	}
	return judge, nil
}

func semanticJudgeConfigHash(cfg config.ChatConfig) string {
	configuration := map[string]any{
		"protocol": cfg.Protocol, "endpoint": strings.TrimRight(cfg.BaseURL, "/") + "/" + strings.TrimLeft(cfg.APIPath, "/"),
		"model": cfg.Model, "timeout": cfg.Timeout.String(), "max_tokens": cfg.MaxTokens,
		"temperature": cfg.Temperature, "reasoning_effort": cfg.ReasoningEffort, "max_retries": cfg.MaxRetries,
	}
	encoded, _ := json.Marshal(configuration)
	hash := sha256.Sum256(encoded)
	return hex.EncodeToString(hash[:])
}

func diagnosisSkillSnapshotHash(method string) (string, error) {
	if domain.IsKubePilotBrainMethod(method) {
		resolver, err := agent.LoadDefaultBrainSkillResolver()
		if err != nil {
			return "", err
		}
		return resolver.SnapshotHash(), nil
	}
	files := []struct{ agent, path string }{
		{"planner_agent", "internal/agent/skills/planner/SKILL.md"},
		{"metric_worker", "internal/agent/skills/metric-worker/SKILL.md"},
		{"log_worker", "internal/agent/skills/log-worker/SKILL.md"},
		{"trace_worker", "internal/agent/skills/trace-worker/SKILL.md"},
		{"topology_worker", "internal/agent/skills/topology-worker/SKILL.md"},
		{"diagnosis_agent", "internal/agent/skills/diagnosis/SKILL.md"},
		{"alternative_agent", "internal/agent/skills/alternative/SKILL.md"},
		{"critic_agent", "internal/agent/skills/critic/SKILL.md"},
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
		"SUPERVISOR_MAX_ITERATIONS": "10", "SUPERVISOR_MAX_TOOL_USES": "50", "SUPERVISOR_MAX_TOKENS": "8192", "SUPERVISOR_MAX_CORRECTIONS": "3",
		"DIAGNOSIS_MAX_ITERATIONS": "18", "DIAGNOSIS_MAX_TOOL_USES": "50", "DIAGNOSIS_MAX_TOKENS": "8192", "DIAGNOSIS_MAX_CORRECTIONS": "3",
		"RECOVERY_MAX_ITERATIONS": "10", "RECOVERY_MAX_TOOL_USES": "50", "RECOVERY_MAX_TOKENS": "8192", "RECOVERY_MAX_CORRECTIONS": "2",
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
		{"chat_protocol", existing.Protocol, current.Protocol}, {"chat_model", existing.Model, current.Model}, {"model_profile", existing.ModelProfile, current.ModelProfile},
		{"semantic_judge", strconv.FormatBool(existing.SemanticJudge), strconv.FormatBool(current.SemanticJudge)}, {"semantic_judge_model", existing.SemanticJudgeModel, current.SemanticJudgeModel}, {"semantic_judge_config_hash", existing.SemanticJudgeConfig, current.SemanticJudgeConfig},
		{"endpoint_hash", existing.EndpointHash, current.EndpointHash}, {"model_config_hash", existing.ModelConfigHash, current.ModelConfigHash},
		{"skill_snapshot_hash", existing.SkillSnapshotHash, current.SkillSnapshotHash}, {"ranking_policy_hash", existing.RankingPolicyHash, current.RankingPolicyHash},
		{"tool_cost_policy_hash", existing.ToolCostPolicyHash, current.ToolCostPolicyHash}, {"budget_config_hash", existing.BudgetConfigHash, current.BudgetConfigHash},
		{"reranker_model", existing.RerankerModel, current.RerankerModel}, {"reranker_config_hash", existing.RerankerConfigHash, current.RerankerConfigHash},
		{"embedding_model", existing.EmbeddingModel, current.EmbeddingModel}, {"embedding_dimensions", existing.EmbeddingDimensions, current.EmbeddingDimensions},
		{"diagnosis_method", existing.DiagnosisMethod, current.DiagnosisMethod}, {"git_commit", existing.GitCommit, current.GitCommit},
		{"causal_mode", existing.CausalMode, current.CausalMode},
		{"source_hash", existing.SourceHash, current.SourceHash}, {"history_dataset_hash", existing.HistoryDatasetHash, current.HistoryDatasetHash},
		{"history_collection", existing.HistoryCollection, current.HistoryCollection},
		{"dataset_split", existing.DatasetSplit, current.DatasetSplit}, {"repetitions", strconv.Itoa(existing.Repetitions), strconv.Itoa(current.Repetitions)},
		{"parallelism", strconv.Itoa(existing.Parallelism), strconv.Itoa(current.Parallelism)}, {"model_concurrency", strconv.Itoa(existing.ModelConcurrency), strconv.Itoa(current.ModelConcurrency)},
		{"worker_namespaces", stableJSON(existing.WorkerNamespaces), stableJSON(current.WorkerNamespaces)}, {"shard_policy", existing.ShardPolicy, current.ShardPolicy},
		{"seeds", stableJSON(existing.Seeds), stableJSON(current.Seeds)}, {"pricing_snapshot", stableJSON(existing.PricingSnapshot), stableJSON(current.PricingSnapshot)},
	}
	for _, item := range fields {
		if item.old != item.new {
			return fmt.Errorf("cannot resume run with changed %s (recorded=%q current=%q); start a new run ID", item.name, item.old, item.new)
		}
	}
	return nil
}

func stableJSON(value any) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func parseSeeds(value string) ([]int64, error) {
	seen := map[int64]bool{}
	var seeds []int64
	for _, part := range strings.Split(value, ",") {
		seed, err := strconv.ParseInt(strings.TrimSpace(part), 10, 64)
		if err != nil || seed <= 0 {
			return nil, fmt.Errorf("invalid seed %q", part)
		}
		if !seen[seed] {
			seen[seed] = true
			seeds = append(seeds, seed)
		}
	}
	if len(seeds) == 0 {
		return nil, fmt.Errorf("at least one seed is required")
	}
	return seeds, nil
}

func selectDatasetSplit(items []scenarios.Scenario, split string) ([]scenarios.Scenario, error) {
	if split == "all" {
		return append([]scenarios.Scenario(nil), items...), nil
	}
	if split != "dev" && split != "validation" && split != "test" {
		return nil, fmt.Errorf("unsupported dataset split %q", split)
	}
	out := make([]scenarios.Scenario, 0, len(items))
	for _, item := range items {
		if item.Split == split {
			out = append(out, item)
		}
	}
	return out, nil
}

func expandScenarioSeeds(items []scenarios.Scenario, seeds []int64, repetitions int) []scenarios.Scenario {
	out := make([]scenarios.Scenario, 0, len(items)*len(seeds)*repetitions)
	for _, item := range items {
		for _, seed := range seeds {
			for repetition := 1; repetition <= repetitions; repetition++ {
				copy := item
				copy.Seed = seed
				copy.Repetition = repetition
				copy.InjectParams = map[string]any{"variant": copy.Variant, "seed": seed, "repetition": repetition}
				out = append(out, copy)
			}
		}
	}
	return out
}

func caseCheckpointKey(caseID string, seed int64, repetition int) string {
	return fmt.Sprintf("%s|%d|%d", caseID, seed, repetition)
}

func comparisonStrategyOrder(runID string) []string {
	return randomizedStrategyOrder([]string{domain.DiagnosisMethodDirect, domain.DiagnosisMethodRAG, domain.DiagnosisMethodReAct, domain.DiagnosisMethodRuleOnly, domain.DiagnosisMethodEvidence, domain.DiagnosisMethodCognitive, domain.DiagnosisMethodActive, domain.DiagnosisMethodKubePilot, domain.DiagnosisMethodKubePilotNoReflection, domain.DiagnosisMethodKubePilotNoOptionalSkills}, runID)
}

func randomizedStrategyOrder(strategies []string, runID string) []string {
	digest := sha256.Sum256([]byte(runID))
	ordered := append([]string(nil), strategies...)
	for index := len(ordered) - 1; index > 0; index-- {
		swap := int(digest[len(ordered)-1-index]) % (index + 1)
		ordered[index], ordered[swap] = ordered[swap], ordered[index]
	}
	return ordered
}

func parseStrategies(value string) ([]string, error) {
	seen := map[string]bool{}
	var strategies []string
	for _, part := range strings.Split(value, ",") {
		strategy, ok := domain.NormalizeDiagnosisMethod(strings.TrimSpace(part))
		if !ok || seen[strategy] {
			return nil, fmt.Errorf("invalid or duplicate strategy %q", part)
		}
		seen[strategy] = true
		strategies = append(strategies, strategy)
	}
	if len(strategies) < 2 {
		return nil, fmt.Errorf("comparison requires at least two strategies")
	}
	return strategies, nil
}

func strategyArchitecture(strategy string) string {
	switch strategy {
	case domain.DiagnosisMethodDirect:
		return "single-pass"
	case domain.DiagnosisMethodRAG:
		return "single-pass-episodic"
	case domain.DiagnosisMethodReAct:
		return "single-react"
	case domain.DiagnosisMethodRuleOnly:
		return "eino-rule-diagnosis-runtime"
	case domain.DiagnosisMethodEvidence:
		return "eino-evidence-diagnosis-runtime"
	case domain.DiagnosisMethodCognitive, domain.DiagnosisMethodActive:
		return domain.WorkflowRuntimeName
	case domain.DiagnosisMethodKubePilot, domain.DiagnosisMethodKubePilotNoReflection, domain.DiagnosisMethodKubePilotNoOptionalSkills:
		return "eino-native-self-reflective-brain"
	default:
		return "unknown"
	}
}

func pricingSnapshot() map[string]float64 {
	prices := map[string]float64{}
	for key, environment := range map[string]string{"input_per_million": "CHAT_INPUT_PRICE_PER_MILLION", "output_per_million": "CHAT_OUTPUT_PRICE_PER_MILLION", "reasoning_per_million": "CHAT_REASONING_PRICE_PER_MILLION"} {
		value, _ := strconv.ParseFloat(os.Getenv(environment), 64)
		prices[key] = value
	}
	return prices
}

func writeDiagnosisComparison(root, profile, runID string, methods []string) error {
	summaries := map[string]reporter.Summary{}
	caseResults := map[string][]reporter.CaseResult{}
	var parent reporter.Manifest
	for _, method := range methods {
		var methodManifest reporter.Manifest
		if err := readJSON(filepath.Join(root, method, "manifest.json"), &methodManifest); err != nil {
			return err
		}
		if methodManifest.RunID != runID || methodManifest.DiagnosisMethod != method {
			return fmt.Errorf("strategy %s artifact does not belong to comparison run %s", method, runID)
		}
		if parent.RunID == "" {
			parent = methodManifest
		} else if err := validateComparisonManifest(parent, methodManifest); err != nil {
			return fmt.Errorf("strategy %s manifest is not paired with the comparison configuration: %w", method, err)
		}
		var summary reporter.Summary
		if err := readJSON(filepath.Join(root, method, "summary.json"), &summary); err != nil {
			return err
		}
		items, err := readCaseResults(filepath.Join(root, method, "cases.jsonl"))
		if err != nil {
			return err
		}
		summaries[method] = summary
		caseResults[method] = items
	}
	comparisonReport, err := benchmarkcomparison.Build(runID, profile, summaries, caseResults)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(root, 0o750); err != nil {
		return err
	}
	parent.DiagnosisMethod = ""
	parent.Strategies = append([]string(nil), methods...)
	parent.Architecture = "paired-strategy-comparison"
	parent.FinishedAt = time.Now().UTC()
	if err = reporter.WriteManifestDir(root, parent); err != nil {
		return err
	}
	b, err := json.MarshalIndent(comparisonReport, "", "  ")
	if err != nil {
		return err
	}
	if err = os.WriteFile(filepath.Join(root, "diagnosis-comparison.json"), b, 0o640); err != nil {
		return err
	}
	if err = writeComparisonCSV(filepath.Join(root, "diagnosis-comparison.csv"), comparisonReport); err != nil {
		return err
	}
	if err = writeSystemCSV(filepath.Join(root, "diagnosis-systems.csv"), comparisonReport); err != nil {
		return err
	}
	if err = writeBreakdownCSV(filepath.Join(root, "diagnosis-breakdowns.csv"), comparisonReport); err != nil {
		return err
	}
	if err = writeComparisonFailures(filepath.Join(root, "failures.json"), caseResults); err != nil {
		return err
	}
	var report strings.Builder
	fmt.Fprintf(&report, "# KubePilot Diagnosis Baseline Comparison\n\n- Run: `%s`\n- Profile: `%s`\n- Valid: `%t`\n\n", runID, profile, comparisonReport.Valid)
	report.WriteString("| System | Strict Diagnosis Accuracy (95% CI) | Recovery Success (95% CI) | Safety Violations | Mean Cost (95% CI) | P95 Latency |\n|---|---:|---:|---:|---:|---:|\n")
	for _, item := range comparisonReport.Systems {
		s := item.Summary
		fmt.Fprintf(&report, "| %s | %.2f%% [%.2f, %.2f] | %.2f%% [%.2f, %.2f] | %d | %.6f [%.6f, %.6f] | %.3fs [%.3f, %.3f] |\n", item.Strategy, item.DiagnosisAccuracy.Estimate*100, item.DiagnosisAccuracy.Lower*100, item.DiagnosisAccuracy.Upper*100, item.RecoverySuccess.Estimate*100, item.RecoverySuccess.Lower*100, item.RecoverySuccess.Upper*100, s.SafetyViolations, item.MeanCost.Estimate, item.MeanCost.Lower, item.MeanCost.Upper, item.P95Latency.Estimate, item.P95Latency.Lower, item.P95Latency.Upper)
	}
	report.WriteString("\n## Paired comparisons against KubePilot\n\n| Baseline | Metric | Absolute difference (95% CI) | Relative change | Test | Holm-adjusted p | Effect size |\n|---|---|---:|---:|---|---:|---:|\n")
	for _, item := range comparisonReport.Comparisons {
		fmt.Fprintf(&report, "| %s | %s | %.4f [%.4f, %.4f] | %.2f%% | %s | %.6f | %.4f |\n", item.Baseline, item.Metric, item.Difference.Estimate, item.Difference.Lower, item.Difference.Upper, item.RelativeImprovement*100, item.Test, item.HolmAdjustedPValue, item.EffectSize)
	}
	report.WriteString("\nInfrastructure failures are excluded from model metrics. The run is invalid if their rate exceeds 2% or any approval bypass, namespace violation, or duplicate mutation is observed. Claims of superiority require the paired 95% CI to exclude zero after retaining all baseline outcomes. Category, root-cause variant, service, and resource slices are stored in `diagnosis-breakdowns.csv` and the JSON report.\n")
	return os.WriteFile(filepath.Join(root, "report.md"), []byte(report.String()), 0o640)
}

func validateComparisonManifest(parent, candidate reporter.Manifest) error {
	type field struct{ name, old, new string }
	fields := []field{
		{"profile", parent.Profile, candidate.Profile},
		{"manifest_hash", parent.ManifestHash, candidate.ManifestHash},
		{"catalog_hash", parent.CatalogHash, candidate.CatalogHash},
		{"chat_protocol", parent.Protocol, candidate.Protocol},
		{"chat_model", parent.Model, candidate.Model},
		{"model_profile", parent.ModelProfile, candidate.ModelProfile},
		{"semantic_judge", strconv.FormatBool(parent.SemanticJudge), strconv.FormatBool(candidate.SemanticJudge)},
		{"semantic_judge_model", parent.SemanticJudgeModel, candidate.SemanticJudgeModel},
		{"semantic_judge_config_hash", parent.SemanticJudgeConfig, candidate.SemanticJudgeConfig},
		{"endpoint_hash", parent.EndpointHash, candidate.EndpointHash},
		{"model_config_hash", parent.ModelConfigHash, candidate.ModelConfigHash},
		{"skill_snapshot_hash", parent.SkillSnapshotHash, candidate.SkillSnapshotHash},
		{"ranking_policy_hash", parent.RankingPolicyHash, candidate.RankingPolicyHash},
		{"tool_cost_policy_hash", parent.ToolCostPolicyHash, candidate.ToolCostPolicyHash},
		{"budget_config_hash", parent.BudgetConfigHash, candidate.BudgetConfigHash},
		{"reranker_model", parent.RerankerModel, candidate.RerankerModel},
		{"reranker_config_hash", parent.RerankerConfigHash, candidate.RerankerConfigHash},
		{"embedding_model", parent.EmbeddingModel, candidate.EmbeddingModel},
		{"embedding_dimensions", parent.EmbeddingDimensions, candidate.EmbeddingDimensions},
		{"causal_mode", parent.CausalMode, candidate.CausalMode},
		{"source_hash", parent.SourceHash, candidate.SourceHash},
		{"history_dataset_hash", parent.HistoryDatasetHash, candidate.HistoryDatasetHash},
		{"history_collection", parent.HistoryCollection, candidate.HistoryCollection},
		{"dataset_split", parent.DatasetSplit, candidate.DatasetSplit},
		{"repetitions", strconv.Itoa(parent.Repetitions), strconv.Itoa(candidate.Repetitions)},
		{"parallelism", strconv.Itoa(parent.Parallelism), strconv.Itoa(candidate.Parallelism)},
		{"model_concurrency", strconv.Itoa(parent.ModelConcurrency), strconv.Itoa(candidate.ModelConcurrency)},
		{"worker_namespaces", stableJSON(parent.WorkerNamespaces), stableJSON(candidate.WorkerNamespaces)},
		{"shard_policy", parent.ShardPolicy, candidate.ShardPolicy},
		{"seeds", stableJSON(parent.Seeds), stableJSON(candidate.Seeds)},
		{"pricing_snapshot", stableJSON(parent.PricingSnapshot), stableJSON(candidate.PricingSnapshot)},
	}
	for _, item := range fields {
		if item.old != item.new {
			return fmt.Errorf("%s differs (left=%q right=%q)", item.name, item.old, item.new)
		}
	}
	return nil
}

func writeSystemCSV(path string, report benchmarkcomparison.Report) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()
	writer := csv.NewWriter(file)
	defer writer.Flush()
	_ = writer.Write([]string{"strategy", "strict_accuracy", "strict_accuracy_ci_lower", "strict_accuracy_ci_upper", "recovery_success", "recovery_ci_lower", "recovery_ci_upper", "safety_violations", "mean_cost", "mean_cost_ci_lower", "mean_cost_ci_upper", "p95_latency_seconds", "p95_latency_ci_lower", "p95_latency_ci_upper", "valid"})
	for _, item := range report.Systems {
		_ = writer.Write([]string{item.Strategy, formatFloat(item.DiagnosisAccuracy.Estimate), formatFloat(item.DiagnosisAccuracy.Lower), formatFloat(item.DiagnosisAccuracy.Upper), formatFloat(item.RecoverySuccess.Estimate), formatFloat(item.RecoverySuccess.Lower), formatFloat(item.RecoverySuccess.Upper), strconv.Itoa(item.Summary.SafetyViolations), formatFloat(item.MeanCost.Estimate), formatFloat(item.MeanCost.Lower), formatFloat(item.MeanCost.Upper), formatFloat(item.P95Latency.Estimate), formatFloat(item.P95Latency.Lower), formatFloat(item.P95Latency.Upper), strconv.FormatBool(item.Summary.Valid)})
	}
	return writer.Error()
}

func writeBreakdownCSV(path string, report benchmarkcomparison.Report) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()
	writer := csv.NewWriter(file)
	defer writer.Flush()
	_ = writer.Write([]string{"strategy", "dimension", "value", "cases", "strict_accuracy", "strict_accuracy_ci_lower", "strict_accuracy_ci_upper", "recovery_success", "recovery_ci_lower", "recovery_ci_upper", "mean_cost", "mean_cost_ci_lower", "mean_cost_ci_upper", "p95_latency_seconds", "p95_latency_ci_lower", "p95_latency_ci_upper"})
	for _, system := range report.Systems {
		for _, item := range system.Breakdowns {
			_ = writer.Write([]string{system.Strategy, item.Dimension, item.Value, strconv.Itoa(item.Cases), formatFloat(item.DiagnosisAccuracy.Estimate), formatFloat(item.DiagnosisAccuracy.Lower), formatFloat(item.DiagnosisAccuracy.Upper), formatFloat(item.RecoverySuccess.Estimate), formatFloat(item.RecoverySuccess.Lower), formatFloat(item.RecoverySuccess.Upper), formatFloat(item.MeanCost.Estimate), formatFloat(item.MeanCost.Lower), formatFloat(item.MeanCost.Upper), formatFloat(item.P95Latency.Estimate), formatFloat(item.P95Latency.Lower), formatFloat(item.P95Latency.Upper)})
		}
	}
	return writer.Error()
}

func writeComparisonFailures(path string, cases map[string][]reporter.CaseResult) error {
	type failure struct {
		Strategy              string `json:"strategy"`
		CaseID                string `json:"case_id"`
		Seed                  int64  `json:"seed"`
		Repetition            int    `json:"repetition"`
		Status                string `json:"status"`
		InfrastructureFailure bool   `json:"infrastructure_failure"`
		SafetyViolation       bool   `json:"safety_violation"`
		ApprovalBypass        bool   `json:"approval_bypass"`
		NamespaceViolation    bool   `json:"namespace_violation"`
		DuplicateMutation     bool   `json:"duplicate_mutation"`
		Error                 string `json:"error,omitempty"`
	}
	var failures []failure
	for strategy, items := range cases {
		for _, item := range items {
			if item.Status == "passed" && !item.InfrastructureFailure && !item.SafetyViolation && item.Score.StrictRootCause && item.VerificationOK {
				continue
			}
			failures = append(failures, failure{Strategy: strategy, CaseID: item.CaseID, Seed: item.Seed, Repetition: item.Repetition, Status: item.Status, InfrastructureFailure: item.InfrastructureFailure, SafetyViolation: item.SafetyViolation, ApprovalBypass: item.ApprovalBypass, NamespaceViolation: item.NamespaceViolation, DuplicateMutation: item.DuplicateMutation, Error: item.Error})
		}
	}
	sort.Slice(failures, func(i, j int) bool {
		left := fmt.Sprintf("%s|%s|%d|%d", failures[i].Strategy, failures[i].CaseID, failures[i].Seed, failures[i].Repetition)
		right := fmt.Sprintf("%s|%s|%d|%d", failures[j].Strategy, failures[j].CaseID, failures[j].Seed, failures[j].Repetition)
		return left < right
	})
	raw, err := json.MarshalIndent(failures, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0o640)
}

func formatFloat(value float64) string {
	return strconv.FormatFloat(value, 'f', 8, 64)
}

func writeComparisonCSV(path string, report benchmarkcomparison.Report) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()
	writer := csv.NewWriter(file)
	defer writer.Flush()
	_ = writer.Write([]string{"baseline", "target", "metric", "pairs", "difference", "ci_lower", "ci_upper", "relative_change", "test", "p_value", "holm_adjusted_p_value", "effect_size"})
	for _, item := range report.Comparisons {
		_ = writer.Write([]string{item.Baseline, item.Target, item.Metric, strconv.Itoa(item.Pairs), strconv.FormatFloat(item.Difference.Estimate, 'f', 8, 64), strconv.FormatFloat(item.Difference.Lower, 'f', 8, 64), strconv.FormatFloat(item.Difference.Upper, 'f', 8, 64), strconv.FormatFloat(item.RelativeImprovement, 'f', 8, 64), item.Test, strconv.FormatFloat(item.PValue, 'f', 8, 64), strconv.FormatFloat(item.HolmAdjustedPValue, 'f', 8, 64), strconv.FormatFloat(item.EffectSize, 'f', 8, 64)})
	}
	return writer.Error()
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
	agentURL := fs.String("agent-url", "http://localhost:8080", "live Agent URL")
	webhookToken := fs.String("webhook-token", os.Getenv("ALERTMANAGER_WEBHOOK_TOKEN"), "Alertmanager webhook token")
	runSalt := fs.String("run-salt", ulid.Make().String(), "unique live-correlation run identifier")
	_ = fs.Parse(args)
	if *output == "" {
		*output = filepath.Join(artifactlayout.RunDirectory("artifacts/benchmark", "correlation", "full", time.Now().UTC()), "correlation-summary.json")
	}
	items := correlation.Generate(*groups, 2, 8, *seed)
	if strings.TrimSpace(*agentURL) == "" {
		fatal(fmt.Errorf("--agent-url is required; correlation has no benchmark-only fallback"))
	}
	actual, err := liveCorrelation(context.Background(), *agentURL, *webhookToken, *runSalt, items)
	fatal(err)
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
	namespaceList := fs.String("namespaces", "", "comma-separated memory scopes; defaults to the dataset namespace")
	output := fs.String("output", "", "seed manifest; defaults to a timestamped history dataset directory")
	_ = fs.Parse(args)
	if *output == "" {
		*output = filepath.Join(artifactlayout.RunDirectory("artifacts/benchmark", "history-seed", "full", time.Now().UTC()), "manifest.json")
	}
	historyCatalog, items, datasetHash, err := history.Load(*dataset)
	fatal(err)
	namespaces := []string{historyCatalog.Namespace}
	if strings.TrimSpace(*namespaceList) != "" {
		namespaces, err = parseNamespaceList(*namespaceList)
		fatal(err)
	}
	items = expandHistoryNamespaces(items, historyCatalog.Namespace, namespaces)
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
		"namespaces":      namespaces,
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

func parseNamespaceList(value string) ([]string, error) {
	seen := map[string]bool{}
	var namespaces []string
	for _, part := range strings.Split(value, ",") {
		namespace := strings.TrimSpace(part)
		if namespace == "" {
			return nil, fmt.Errorf("namespace list contains an empty entry")
		}
		if seen[namespace] {
			return nil, fmt.Errorf("namespace %q is duplicated", namespace)
		}
		seen[namespace] = true
		namespaces = append(namespaces, namespace)
	}
	return namespaces, nil
}

func expandHistoryNamespaces(items []history.SeedDocument, sourceNamespace string, namespaces []string) []history.SeedDocument {
	out := make([]history.SeedDocument, 0, len(items)*len(namespaces))
	for _, namespace := range namespaces {
		for _, item := range items {
			copy := item
			if namespace != sourceNamespace {
				copy.Document.ID = item.Document.ID + "-" + namespace
			}
			copy.Document.Namespace = namespace
			copy.Text = strings.ReplaceAll(item.Text, sourceNamespace, namespace)
			out = append(out, copy)
		}
	}
	return out
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
			fatal(fmt.Errorf("run %q was not found under %s", *runID, *root))
		}
	}
	var manifest reporter.Manifest
	fatal(readJSON(filepath.Join(*artifactDir, "manifest.json"), &manifest))
	method := manifest.DiagnosisMethod
	if method == "" {
		method = domain.DiagnosisMethodKubePilot
	}
	seeds := manifest.Seeds
	if len(seeds) == 0 {
		seeds = []int64{manifest.Seed}
	}
	seedValues := make([]string, 0, len(seeds))
	for _, seed := range seeds {
		seedValues = append(seedValues, strconv.FormatInt(seed, 10))
	}
	repetitions := manifest.Repetitions
	if repetitions <= 0 {
		repetitions = 1
	}
	split := manifest.DatasetSplit
	if split == "" {
		split = "all"
	}
	parallelism := manifest.Parallelism
	if parallelism < 1 {
		parallelism = 1
	}
	modelConcurrency := manifest.ModelConcurrency
	if modelConcurrency < 1 {
		modelConcurrency = parallelism
	}
	runArgs := []string{"--profile", manifest.Profile, "--run-id", *runID, "--artifacts", *root, "--artifact-dir", *artifactDir, "--agent-url", *agentURL, "--token", *token, "--kubeconfig", *kubeconfig, "--resume=true", "--auto-approve=" + strconv.FormatBool(*auto), "--diagnosis-method", method, "--causal-mode", manifest.CausalMode, "--dataset-split", split, "--seeds", strings.Join(seedValues, ","), "--repetitions", strconv.Itoa(repetitions), "--workers", strconv.Itoa(parallelism), "--model-concurrency", strconv.Itoa(modelConcurrency), "--worker-namespaces", strings.Join(manifest.WorkerNamespaces, ",")}
	if len(manifest.Strategies) > 1 {
		runArgs = append(runArgs, "--compare-methods=true", "--strategies", strings.Join(manifest.Strategies, ","))
	}
	run(runArgs)
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
			fatal(fmt.Errorf("run %q was not found under %s", *runID, *root))
		}
	}
	var manifest reporter.Manifest
	fatal(readJSON(filepath.Join(dir, "manifest.json"), &manifest))
	if len(manifest.Strategies) > 1 {
		fatal(writeDiagnosisComparison(dir, manifest.Profile, manifest.RunID, manifest.Strategies))
		fmt.Println(filepath.Join(dir, "report.md"))
		return
	}
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
	if err = json.NewEncoder(f).Encode(result); err != nil {
		return err
	}
	return f.Sync()
}

type checkpointRecorder struct {
	path   string
	cancel context.CancelFunc
	mu     sync.Mutex
	err    error
}

func (r *checkpointRecorder) Record(result reporter.CaseResult) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return
	}
	if err := appendCheckpoint(r.path, result); err != nil {
		r.err = fmt.Errorf("append benchmark checkpoint: %w", err)
		if r.cancel != nil {
			r.cancel()
		}
	}
}

func (r *checkpointRecorder) Err() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.err
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
	comparisonFound := false
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || filepath.Base(path) != "manifest.json" {
			return nil
		}
		var manifest reporter.Manifest
		if err := readJSON(path, &manifest); err == nil && manifest.RunID == runID {
			if len(manifest.Strategies) > 1 {
				found = filepath.Dir(path)
				comparisonFound = true
			} else if !comparisonFound && found == "" {
				found = filepath.Dir(path)
			}
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

func envBool(key string, fallback bool) bool {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return fallback
	}
	return parsed
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
