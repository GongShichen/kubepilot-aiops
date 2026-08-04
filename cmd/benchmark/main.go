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
	"syscall"
	"time"

	"github.com/kubepilot-aiops/kubepilot/benchmark/correlation"
	"github.com/kubepilot-aiops/kubepilot/benchmark/datasets"
	"github.com/kubepilot-aiops/kubepilot/benchmark/history"
	"github.com/kubepilot-aiops/kubepilot/benchmark/injector"
	"github.com/kubepilot-aiops/kubepilot/benchmark/reporter"
	"github.com/kubepilot-aiops/kubepilot/benchmark/retrievalbench"
	"github.com/kubepilot-aiops/kubepilot/benchmark/runner"
	"github.com/kubepilot-aiops/kubepilot/benchmark/scenarios"
	"github.com/kubepilot-aiops/kubepilot/benchmark/scorer"
	"github.com/kubepilot-aiops/kubepilot/internal/config"
	"github.com/kubepilot-aiops/kubepilot/internal/domain"
	llm "github.com/kubepilot-aiops/kubepilot/internal/model"
	rerankerclient "github.com/kubepilot-aiops/kubepilot/internal/retrieval/reranker"
	"github.com/kubepilot-aiops/kubepilot/retrieval"
	"github.com/kubepilot-aiops/kubepilot/tools"
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
	case "run":
		run(os.Args[2:])
	case "generate-logs":
		generateLogs(os.Args[2:])
	case "correlation":
		runCorrelation(os.Args[2:])
	case "retrieval":
		runRetrieval(os.Args[2:])
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
	fmt.Fprintln(os.Stderr, "kubepilot-benchmark <validate|run|resume|report|correlation|retrieval|seed-history|generate-logs>")
}
func validate(args []string) {
	fs := flag.NewFlagSet("validate", flag.ExitOnError)
	catalog := fs.String("catalog", "benchmark/incidents.yaml", "scenario catalog")
	_ = fs.Parse(args)
	_, items, hash, err := scenarios.Load(*catalog)
	fatal(err)
	fmt.Printf("valid: %d scenarios, catalog_sha256=%s\n", len(items), hash)
}
func run(args []string) {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	catalog := fs.String("catalog", "benchmark/incidents.yaml", "scenario catalog")
	profile := fs.String("profile", "smoke", "smoke, ci, standard or robustness")
	agentURL := fs.String("agent-url", "http://localhost:8080", "agent URL")
	token := fs.String("token", os.Getenv("API_TOKEN"), "agent token")
	kubeconfig := fs.String("kubeconfig", os.Getenv("KUBECONFIG"), "kubeconfig path")
	artifactRoot := fs.String("artifacts", "artifacts/benchmark", "artifact root")
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
	if *compareMethods {
		if *profile != "smoke" && *profile != "ci" && *profile != "standard" {
			fatal(fmt.Errorf("--compare-methods supports smoke, ci, or standard profiles"))
		}
		if *runID == "" {
			*runID = ulid.Make().String()
		}
		methods := []string{domain.DiagnosisMethodLLMOnly, domain.DiagnosisMethodVectorRAG, domain.DiagnosisMethodKubePilot}
		for _, method := range methods {
			run([]string{"--profile", *profile, "--run-id", *runID + "-" + method, "--catalog", *catalog, "--agent-url", *agentURL, "--token", *token, "--kubeconfig", *kubeconfig, "--artifacts", *artifactRoot, "--auto-approve=" + strconv.FormatBool(*autoApprove), "--dry-run-injector=" + strconv.FormatBool(*dryRun), "--diagnosis-method", method})
			if runCtx.Err() != nil {
				fmt.Fprintln(os.Stderr, "benchmark interrupted after cleaning the active case")
				return
			}
		}
		fatal(writeDiagnosisComparison(*artifactRoot, *runID, *profile, methods))
		fmt.Printf("diagnosis comparison artifacts=%s\n", filepath.Join(*artifactRoot, *runID))
		return
	}
	if *profile == "correlation" {
		runCorrelation([]string{"--output", filepath.Join(*artifactRoot, "correlation-summary.json")})
		return
	}
	if *profile == "retrieval" {
		runRetrieval([]string{"--corpus", filepath.Join(*artifactRoot, "retrieval-500k.jsonl"), "--output", filepath.Join(*artifactRoot, "retrieval")})
		return
	}
	if *profile == "full" {
		if *runID == "" {
			*runID = ulid.Make().String()
		}
		standardArgs := []string{"--profile", "standard", "--run-id", *runID, "--catalog", *catalog, "--agent-url", *agentURL, "--token", *token, "--kubeconfig", *kubeconfig, "--artifacts", *artifactRoot, "--auto-approve=" + strconv.FormatBool(*autoApprove)}
		run(standardArgs)
		runCorrelation([]string{"--output", filepath.Join(*artifactRoot, *runID, "correlation-summary.json")})
		runRetrieval([]string{"--corpus", filepath.Join(*artifactRoot, *runID, "retrieval-500k.jsonl"), "--output", filepath.Join(*artifactRoot, *runID, "retrieval")})
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
	checkpoint := filepath.Join(*artifactRoot, *runID, "checkpoint.jsonl")
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
		fatal(readJSON(filepath.Join(*artifactRoot, *runID, "manifest.json"), &existing))
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
		fatal(reporter.WriteManifest(*artifactRoot, manifest))
	}
	client := runner.NewHTTPClient(*agentURL, *token)
	client.DiagnosisMethod = *diagnosisMethod
	r := &runner.Runner{Registry: reg, Client: client, AutoApprove: *autoApprove, PollInterval: time.Second, DiagnosisMethod: *diagnosisMethod, OnResult: func(result reporter.CaseResult) { fatal(appendCheckpoint(checkpoint, result)) }}
	results := append(previous, r.Run(runCtx, items)...)
	summary, err := reporter.Write(*artifactRoot, manifest, results)
	fatal(err)
	fmt.Printf("run=%s total=%d passed=%d root_cause_accuracy=%.2f%% artifacts=%s\n", manifest.RunID, summary.Total, summary.Passed, summary.RootCauseAccuracy*100, filepath.Join(*artifactRoot, manifest.RunID))
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
		"temperature": env("CHAT_TEMPERATURE", "0"), "reasoning_effort": os.Getenv("CHAT_REASONING_EFFORT"), "max_retries": env("CHAT_MAX_RETRIES", "1"),
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
		"DIAGNOSIS_MAX_ITERATIONS": "12", "DIAGNOSIS_MAX_TOOL_USES": "15", "DIAGNOSIS_MAX_TOOL_COST": "32", "DIAGNOSIS_MAX_TOKENS": "30000", "DIAGNOSIS_MAX_CORRECTIONS": "3",
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

func writeDiagnosisComparison(root, runID, profile string, methods []string) error {
	type row struct {
		Method  string           `json:"method"`
		Summary reporter.Summary `json:"summary"`
	}
	rows := make([]row, 0, len(methods))
	for _, method := range methods {
		var summary reporter.Summary
		if err := readJSON(filepath.Join(root, runID+"-"+method, "summary.json"), &summary); err != nil {
			return err
		}
		rows = append(rows, row{Method: method, Summary: summary})
	}
	dir := filepath.Join(root, runID)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	b, err := json.MarshalIndent(map[string]any{"run_id": runID, "profile": profile, "methods": rows}, "", "  ")
	if err != nil {
		return err
	}
	if err = os.WriteFile(filepath.Join(dir, "diagnosis-comparison.json"), b, 0o640); err != nil {
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
	return os.WriteFile(filepath.Join(dir, "report.md"), []byte(report.String()), 0o640)
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
	output := fs.String("output", "artifacts/benchmark/retrieval-500k.jsonl", "output JSONL")
	count := fs.Int("count", 500000, "record count")
	seed := fs.Uint64("seed", 20260803, "seed")
	_ = fs.Parse(args)
	fatal(os.MkdirAll(filepath.Dir(*output), 0o750))
	fatal(datasets.GenerateLogs(*output, *count, *seed))
	fmt.Printf("generated %d records at %s\n", *count, *output)
}

func runCorrelation(args []string) {
	fs := flag.NewFlagSet("correlation", flag.ExitOnError)
	output := fs.String("output", "artifacts/benchmark/correlation-summary.json", "summary path")
	groups := fs.Int("groups", 100, "ground-truth groups")
	seed := fs.Uint64("seed", 20260803, "seed")
	agentURL := fs.String("agent-url", "", "optional live Agent URL")
	webhookToken := fs.String("webhook-token", os.Getenv("ALERTMANAGER_WEBHOOK_TOKEN"), "Alertmanager webhook token")
	runSalt := fs.String("run-salt", ulid.Make().String(), "unique live-correlation run identifier")
	_ = fs.Parse(args)
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

func runRetrieval(args []string) {
	fs := flag.NewFlagSet("retrieval", flag.ExitOnError)
	corpus := fs.String("corpus", "artifacts/benchmark/retrieval-500k.jsonl", "fixed-seed corpus")
	output := fs.String("output", "artifacts/benchmark/retrieval", "artifact directory")
	count := fs.Int("count", 500000, "corpus size")
	seed := fs.Uint64("seed", 20260803, "seed")
	datasetRun := fs.String("run-id", ulid.Make().String(), "retrieval dataset isolation ID")
	lokiURL := fs.String("loki-url", env("LOKI_URL", "http://localhost:3100"), "Loki URL")
	drainURL := fs.String("drain3-url", env("DRAIN3_WS_URL", "ws://localhost:8081/ws/v1/parse"), "Drain3 WebSocket URL")
	milvusURL := fs.String("milvus-url", env("MILVUS_ADDRESS", "localhost:19530"), "Milvus address")
	dimensions := fs.Int("dimensions", envInt("EMBEDDING_DIMENSIONS", 1024), "embedding dimensions")
	embeddingBatchSize := fs.Int("embedding-batch-size", envInt("EMBEDDING_BATCH_SIZE", 10), "maximum texts per embedding request")
	embeddingRequestInterval := fs.Duration("embedding-request-interval", envDuration("EMBEDDING_REQUEST_INTERVAL", time.Second), "minimum interval between embedding requests")
	_ = fs.Parse(args)
	embedCfg := config.EmbeddingConfig{BaseURL: os.Getenv("EMBEDDING_BASE_URL"), APIPath: env("EMBEDDING_API_PATH", "/embeddings"), APIKey: os.Getenv("EMBEDDING_API_KEY"), Model: os.Getenv("EMBEDDING_MODEL"), Dimensions: *dimensions, Timeout: 30 * time.Second, RequestInterval: *embeddingRequestInterval}
	if embedCfg.BaseURL == "" || embedCfg.APIKey == "" || embedCfg.Model == "" {
		fatal(fmt.Errorf("EMBEDDING_BASE_URL, EMBEDDING_API_KEY and EMBEDDING_MODEL are required for retrieval benchmark"))
	}
	startedAt := time.Now().UTC()
	collection := retrievalCollectionName(embedCfg.Model, *dimensions, *datasetRun)
	parser := retrieval.NewWSParser(*drainURL, os.Getenv("DRAIN3_TOKEN"))
	defer parser.Close()
	rerankerCfg := config.RerankerConfig{
		Enabled:          strings.EqualFold(env("RERANKER_ENABLED", "false"), "true"),
		Protocol:         env("RERANKER_PROTOCOL", "openai-compatible"),
		BaseURL:          os.Getenv("RERANKER_BASE_URL"),
		APIPath:          env("RERANKER_API_PATH", "/reranks"),
		APIKey:           os.Getenv("RERANKER_API_KEY"),
		Model:            os.Getenv("RERANKER_MODEL"),
		Timeout:          envDuration("RERANKER_TIMEOUT", 30*time.Second),
		MaxRetries:       envInt("RERANKER_MAX_RETRIES", 1),
		MaxDocumentBytes: envInt("RERANKER_MAX_DOCUMENT_BYTES", 8192),
		MaxPayloadBytes:  envInt("RERANKER_MAX_PAYLOAD_BYTES", 1048576),
	}
	if err := config.ValidateReranker(rerankerCfg); err != nil {
		fatal(err)
	}
	var neuralReranker rerankerclient.Service
	if rerankerCfg.Enabled {
		neuralReranker = rerankerclient.New(rerankerCfg)
	}
	summary, err := retrievalbench.Run(context.Background(), retrievalbench.Config{
		Corpus: *corpus, OutputDir: *output, DatasetRun: *datasetRun, Count: *count,
		Seed: *seed, EmbeddingBatchSize: *embeddingBatchSize,
		Loki: tools.NewLoki(*lokiURL), Parser: parser, Embedder: llm.NewEmbedder(embedCfg),
		Milvus:            retrieval.NewMilvusStore(*milvusURL, collection, *dimensions),
		CausalPatternFile: env("CAUSAL_PATTERN_FILE", "knowledge/causal_patterns.yaml"),
		RankingPolicyFile: env("RANKING_POLICY_FILE", "knowledge/ranking_policy.yaml"),
		Reranker:          neuralReranker,
		Progress: func(stage string, current, total int) {
			fmt.Printf("stage=%s progress=%d/%d\n", stage, current, total)
		},
	})
	fatal(err)
	corpusHash, err := fileSHA256(*corpus)
	fatal(err)
	rankingPolicyHash, err := fileSHA256(env("RANKING_POLICY_FILE", "knowledge/ranking_policy.yaml"))
	fatal(err)
	endpointHash := sha256.Sum256([]byte(embedCfg.BaseURL))
	manifest := map[string]any{
		"profile": "retrieval", "run_id": *datasetRun, "git_commit": gitCommit(), "seed": *seed,
		"records": summary.Records, "queries": summary.Queries,
		"ground_truth_templates": summary.GroundTruthTemplates, "drain3_clusters": summary.Drain3Clusters,
		"embedding_model": embedCfg.Model, "embedding_dimensions": *dimensions,
		"embedding_batch_size": *embeddingBatchSize, "embedding_request_interval": embeddingRequestInterval.String(),
		"embedding_endpoint_hash": hex.EncodeToString(endpointHash[:]), "milvus_collection": collection,
		"corpus_sha256": corpusHash, "ranking_policy_hash": rankingPolicyHash, "started_at": startedAt, "finished_at": time.Now().UTC(),
	}
	if neuralReranker != nil {
		manifest["reranker_model"] = rerankerCfg.Model
		manifest["reranker_config_hash"] = neuralReranker.ConfigHash()
	}
	b, err := json.MarshalIndent(manifest, "", "  ")
	fatal(err)
	fatal(os.WriteFile(filepath.Join(*output, "manifest.json"), b, 0o640))
	fatal(writeRetrievalReport(*output, *datasetRun, summary))
	fmt.Printf("records=%d ground_truth_templates=%d drain3_clusters=%d queries=%d drain3_compression=%.2f%% output=%s\n", summary.Records, summary.GroundTruthTemplates, summary.Drain3Clusters, summary.Queries, summary.Drain3CompressionRate*100, *output)
}

func writeRetrievalReport(output, runID string, summary retrievalbench.Summary) error {
	latency, err := os.Create(filepath.Join(output, "latency-percentiles.csv"))
	if err != nil {
		return err
	}
	w := csv.NewWriter(latency)
	_ = w.Write([]string{"strategy", "p50_ms", "p95_ms", "p99_ms", "backend_p50_ms", "backend_p95_ms", "backend_p99_ms", "average_candidates", "candidate_reduction"})
	for _, metric := range summary.Metrics {
		_ = w.Write([]string{
			metric.Strategy, fmt.Sprintf("%.3f", metric.P50MS), fmt.Sprintf("%.3f", metric.P95MS), fmt.Sprintf("%.3f", metric.P99MS),
			fmt.Sprintf("%.3f", metric.BackendP50MS), fmt.Sprintf("%.3f", metric.BackendP95MS), fmt.Sprintf("%.3f", metric.BackendP99MS),
			fmt.Sprintf("%.3f", metric.AverageCandidates), fmt.Sprintf("%.6f", metric.CandidateReduction),
		})
		stages := make([]string, 0, len(metric.StageLatency))
		for stage := range metric.StageLatency {
			stages = append(stages, stage)
		}
		sort.Strings(stages)
		for _, stage := range stages {
			values := metric.StageLatency[stage]
			_ = w.Write([]string{
				metric.Strategy + ":" + stage,
				fmt.Sprintf("%.3f", values.P50MS), fmt.Sprintf("%.3f", values.P95MS), fmt.Sprintf("%.3f", values.P99MS),
				"", "", "", "", "",
			})
		}
	}
	w.Flush()
	closeErr := latency.Close()
	if err = w.Error(); err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	var report strings.Builder
	fmt.Fprintf(&report, "# KubePilot Retrieval Benchmark\n\n- Run: `%s`\n- Records: %d\n- Ground-truth templates: %d\n- Drain3 clusters: %d\n- Drain3 compression: %.2f%%\n- Drain3 cluster purity: %.2f%%\n- Queries: %d\n- Embedding calls: %d\n\n", runID, summary.Records, summary.GroundTruthTemplates, summary.Drain3Clusters, summary.Drain3CompressionRate*100, summary.Drain3ClusterPurity*100, summary.Queries, summary.EmbeddingCalls)
	report.WriteString("| Strategy | Recall@1 | Recall@5 | Recall@10 | MRR | NDCG | P50 ms | P95 ms | P99 ms | Candidate reduction |\n|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|\n")
	for _, metric := range summary.Metrics {
		fmt.Fprintf(&report, "| %s | %.2f%% | %.2f%% | %.2f%% | %.4f | %.4f | %.3f | %.3f | %.3f | %.2f%% |\n", metric.Strategy, metric.Recall1*100, metric.Recall5*100, metric.Recall10*100, metric.MRR, metric.NDCG, metric.P50MS, metric.P95MS, metric.P99MS, metric.CandidateReduction*100)
	}
	for _, metric := range summary.Metrics {
		if len(metric.StageLatency) == 0 {
			continue
		}
		report.WriteString("\n### " + metric.Strategy + " stage latency\n\n| Stage | P50 ms | P95 ms | P99 ms |\n|---|---:|---:|---:|\n")
		stages := make([]string, 0, len(metric.StageLatency))
		for stage := range metric.StageLatency {
			stages = append(stages, stage)
		}
		sort.Strings(stages)
		for _, stage := range stages {
			values := metric.StageLatency[stage]
			fmt.Fprintf(&report, "| %s | %.3f | %.3f | %.3f |\n", stage, values.P50MS, values.P95MS, values.P99MS)
		}
	}
	report.WriteString("\nAll values are measured from this run. API keys and complete endpoint URLs are not recorded.\n")
	return os.WriteFile(filepath.Join(output, "report.md"), []byte(report.String()), 0o640)
}

func retrievalCollectionName(model string, dimensions int, runID string) string {
	hash := sha256.Sum256([]byte(model + "\x00" + runID))
	return fmt.Sprintf("kubepilot_benchmark_logs_%d_%s", dimensions, hex.EncodeToString(hash[:6]))
}

func seedHistory(args []string) {
	fs := flag.NewFlagSet("seed-history", flag.ExitOnError)
	dataset := fs.String("dataset", "benchmark/history.yaml", "held-out historical incident dataset")
	milvusURL := fs.String("milvus-url", env("MILVUS_ADDRESS", "localhost:19530"), "Milvus address")
	collection := fs.String("collection", env("HISTORY_COLLECTION", "kubepilot_history"), "isolated history collection")
	dimensions := fs.Int("dimensions", envInt("EMBEDDING_DIMENSIONS", 1024), "embedding dimensions")
	batchSize := fs.Int("embedding-batch-size", envInt("EMBEDDING_BATCH_SIZE", 10), "maximum texts per embedding request")
	requestInterval := fs.Duration("embedding-request-interval", envDuration("EMBEDDING_REQUEST_INTERVAL", time.Second), "minimum interval between embedding requests")
	output := fs.String("output", "artifacts/benchmark/history-seed.json", "seed manifest")
	_ = fs.Parse(args)
	_, items, datasetHash, err := history.Load(*dataset)
	fatal(err)
	embedCfg := config.EmbeddingConfig{
		BaseURL: os.Getenv("EMBEDDING_BASE_URL"), APIPath: env("EMBEDDING_API_PATH", "/embeddings"),
		APIKey: os.Getenv("EMBEDDING_API_KEY"), Model: os.Getenv("EMBEDDING_MODEL"),
		Dimensions: *dimensions, Timeout: 30 * time.Second, RequestInterval: *requestInterval,
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
	agentURL := fs.String("agent-url", "http://localhost:8080", "agent URL")
	token := fs.String("token", os.Getenv("API_TOKEN"), "agent token")
	kubeconfig := fs.String("kubeconfig", os.Getenv("KUBECONFIG"), "kubeconfig")
	auto := fs.Bool("auto-approve", false, "safe auto approval")
	_ = fs.Parse(args)
	if *runID == "" {
		fatal(fmt.Errorf("--run-id is required"))
	}
	var manifest reporter.Manifest
	fatal(readJSON(filepath.Join(*root, *runID, "manifest.json"), &manifest))
	method := manifest.DiagnosisMethod
	if method == "" {
		method = domain.DiagnosisMethodKubePilot
	}
	run([]string{"--profile", manifest.Profile, "--run-id", *runID, "--artifacts", *root, "--agent-url", *agentURL, "--token", *token, "--kubeconfig", *kubeconfig, "--resume=true", "--auto-approve=" + strconv.FormatBool(*auto), "--diagnosis-method", method})
}

func report(args []string) {
	fs := flag.NewFlagSet("report", flag.ExitOnError)
	runID := fs.String("run-id", "", "run ID")
	root := fs.String("artifacts", "artifacts/benchmark", "artifact root")
	_ = fs.Parse(args)
	if *runID == "" {
		fatal(fmt.Errorf("--run-id is required"))
	}
	dir := filepath.Join(*root, *runID)
	var manifest reporter.Manifest
	fatal(readJSON(filepath.Join(dir, "manifest.json"), &manifest))
	items, err := readCaseResults(filepath.Join(dir, "cases.jsonl"))
	fatal(err)
	_, err = reporter.Write(*root, manifest, items)
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
