package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	agentbench "github.com/kubepilot-aiops/kubepilot/benchmark/agent"
	artifactlayout "github.com/kubepilot-aiops/kubepilot/benchmark/artifactlayout"
	incidentretrieval "github.com/kubepilot-aiops/kubepilot/benchmark/incident_retrieval"
	logretrieval "github.com/kubepilot-aiops/kubepilot/benchmark/log_retrieval"
	benchmarkmanifests "github.com/kubepilot-aiops/kubepilot/benchmark/manifests"
	recoverybench "github.com/kubepilot-aiops/kubepilot/benchmark/recovery"
	"github.com/kubepilot-aiops/kubepilot/benchmark/reporter"
	benchmarkreports "github.com/kubepilot-aiops/kubepilot/benchmark/reports"
	"github.com/kubepilot-aiops/kubepilot/internal/config"
	rerankerclient "github.com/kubepilot-aiops/kubepilot/internal/retrieval/reranker"
	"github.com/kubepilot-aiops/kubepilot/retrieval"
	"github.com/kubepilot-aiops/kubepilot/tools"
)

func runLogRetrieval(args []string) {
	fs := flag.NewFlagSet("log-retrieval", flag.ExitOnError)
	corpus := fs.String("corpus", "", "fixed-seed log corpus")
	output := fs.String("output", "", "artifact directory; defaults to a timestamped log-retrieval directory")
	count := fs.Int("count", 500000, "corpus size")
	seed := fs.Uint64("seed", 20260803, "dataset seed")
	runID := fs.String("run-id", "", "isolated dataset run ID")
	lokiURL := fs.String("loki-url", env("LOKI_URL", "http://localhost:3200"), "isolated Loki URL")
	drainURL := fs.String("drain3-url", env("DRAIN3_WS_URL", "ws://localhost:8181/ws/v1/parse"), "isolated Drain3 URL")
	_ = fs.Parse(args)
	if *output == "" {
		*output = artifactlayout.RunDirectory("artifacts/benchmark", "log-retrieval", "full", time.Now().UTC())
	}
	if *corpus == "" {
		*corpus = filepath.Join(*output, "log-retrieval-500k.jsonl")
	}
	if *runID == "" {
		*runID = time.Now().UTC().Format("20060102T150405.000000000Z")
	}
	parser := retrieval.NewWSParser(*drainURL, os.Getenv("DRAIN3_TOKEN"))
	defer parser.Close()
	summary, err := logretrieval.Run(context.Background(), logretrieval.Config{
		Corpus: *corpus, Count: *count, Seed: *seed, DatasetRun: *runID,
		Loki: tools.NewLoki(*lokiURL), Parser: parser, OutputDir: *output,
		Progress: func(stage string, current, total int) {
			fmt.Printf("suite=log_retrieval stage=%s progress=%d/%d\n", stage, current, total)
		},
	})
	fatal(err)
	manifest := map[string]any{"suite": "log_retrieval", "run_id": *runID, "records": summary.Records, "queries": summary.Queries, "seed": *seed, "git_commit": gitCommit(), "started_at": summary.StartedAt, "finished_at": summary.FinishedAt}
	fatal(writeSuiteManifest(filepath.Join(*output, "manifest.json"), manifest))
	fatal(benchmarkreports.WriteEnvelope(filepath.Join(*output, "benchmark_report.json"), benchmarkreports.Envelope{
		Benchmark: "log_retrieval",
		Manifest:  runtimeManifest(),
		Dataset:   benchmarkreports.DatasetInfo{Name: "log-retrieval", Size: summary.Records},
		Metrics:   summary.Metrics,
		Cases:     summary.Queries,
	}))
	fmt.Printf("suite=log_retrieval records=%d queries=%d templates=%d clusters=%d output=%s\n", summary.Records, summary.Queries, summary.GroundTruthTemplates, summary.Drain3Clusters, *output)
}

func runIncidentRetrieval(args []string) {
	fs := flag.NewFlagSet("incident-retrieval", flag.ExitOnError)
	dataset := fs.String("dataset", "benchmark/datasets/incidents/structured.yaml", "structured incident dataset")
	count := fs.Int("count", envInt("INCIDENT_RETRIEVAL_QUERIES", 500), "number of structured incident queries")
	output := fs.String("output", "", "artifact directory; defaults to a timestamped incident-retrieval directory")
	_ = fs.Parse(args)
	if *output == "" {
		*output = artifactlayout.RunDirectory("artifacts/benchmark", "incident-retrieval", "full", time.Now().UTC())
	}
	service := newBenchmarkReranker()
	report, err := incidentretrieval.Run(context.Background(), incidentretrieval.RunnerConfig{DatasetPath: *dataset, Count: *count, OutputDir: *output, Reranker: service, Progress: func(current, total int) { fmt.Printf("suite=incident_retrieval progress=%d/%d\n", current, total) }})
	fatal(err)
	fatal(writeSuiteManifest(filepath.Join(*output, "manifest.json"), map[string]any{"suite": "incident_retrieval", "dataset": *dataset, "queries": report.Queries(), "strategies": len(report.Strategies), "category_counts": report.CategoryCounts, "git_commit": gitCommit()}))
	fatal(benchmarkreports.WriteEnvelope(filepath.Join(*output, "benchmark_report.json"), benchmarkreports.Envelope{
		Benchmark: "incident_retrieval",
		Manifest:  runtimeManifest(),
		Dataset:   benchmarkreports.DatasetInfo{Name: "incident-retrieval", Size: report.Queries(), CategoryCounts: report.CategoryCounts},
		Metrics:   report,
		Cases:     report.Queries(),
	}))
	fmt.Printf("suite=incident_retrieval queries=%d strategies=%d output=%s\n", report.Queries(), len(report.Strategies), *output)
}

func runAgentReport(args []string) {
	fs := flag.NewFlagSet("agent-report", flag.ExitOnError)
	input := fs.String("input", "", "completed diagnosis cases.jsonl")
	output := fs.String("output", "", "report path; defaults to a timestamped agent directory")
	_ = fs.Parse(args)
	if *input == "" {
		fatal(fmt.Errorf("--input is required"))
	}
	if *output == "" {
		*output = filepath.Join(artifactlayout.RunDirectory("artifacts/benchmark", "agent", "full", time.Now().UTC()), "agent_behavior_report.json")
	}
	items := loadCaseResults(*input)
	metrics := agentbench.EvaluateCaseResults(items)
	fatal(benchmarkreports.WriteEnvelope(*output, benchmarkreports.Envelope{
		Benchmark:   "agent_behavior",
		Manifest:    runtimeManifest(),
		Dataset:     benchmarkreports.DatasetInfo{Name: "diagnosis-agent", Size: len(items)},
		Metrics:     metrics,
		Cases:       len(items),
		Limitations: []string{"Behavior metrics are derived from the persisted observations of the live public Agent run."},
	}))
	fmt.Printf("suite=agent_behavior cases=%d output=%s\n", len(items), *output)
}

func runRecoveryReport(args []string) {
	fs := flag.NewFlagSet("recovery-report", flag.ExitOnError)
	input := fs.String("input", "", "completed diagnosis cases.jsonl")
	count := fs.Int("count", 50, "number of recovery cases to evaluate")
	output := fs.String("output", "", "report path; defaults to a timestamped recovery directory")
	_ = fs.Parse(args)
	if *input == "" {
		fatal(fmt.Errorf("--input is required"))
	}
	if *output == "" {
		*output = filepath.Join(artifactlayout.RunDirectory("artifacts/benchmark", "recovery", "full", time.Now().UTC()), "recovery_report.json")
	}
	items := loadCaseResults(*input)
	if *count > 0 && len(items) > *count {
		items = items[:*count]
	}
	metrics := recoverybench.EvaluateCaseResults(items)
	fatal(benchmarkreports.WriteEnvelope(*output, benchmarkreports.Envelope{
		Benchmark:   "recovery",
		Manifest:    runtimeManifest(),
		Dataset:     benchmarkreports.DatasetInfo{Name: "diagnosis-recovery", Size: len(items)},
		Metrics:     metrics,
		Cases:       len(items),
		Limitations: []string{"Recovery observations come from real proposals, approvals, dry-runs and verification in the live Agent run; no mutation is synthesized by the evaluator."},
	}))
	fmt.Printf("suite=recovery cases=%d output=%s\n", len(items), *output)
}

func runAutonomousReport(args []string) {
	fs := flag.NewFlagSet("autonomous-report", flag.ExitOnError)
	diagnosis := fs.String("diagnosis", "", "diagnosis summary.json")
	agent := fs.String("agent", "", "agent behavior report")
	recovery := fs.String("recovery", "", "recovery report")
	knowledge := fs.String("knowledge", "", "knowledge report")
	logRetrieval := fs.String("log-retrieval", "", "log retrieval report")
	incidentRetrieval := fs.String("incident-retrieval", "", "incident retrieval report")
	output := fs.String("output", "", "report path; defaults to a timestamped autonomous directory")
	_ = fs.Parse(args)
	if *diagnosis == "" {
		fatal(fmt.Errorf("--diagnosis is required"))
	}
	if *agent == "" {
		*agent = latestArtifactFile("artifacts/benchmark", "agent", "agent_behavior_report.json")
	}
	if *recovery == "" {
		*recovery = latestArtifactFile("artifacts/benchmark", "recovery", "recovery_report.json")
	}
	if *knowledge == "" {
		*knowledge = latestArtifactFile("artifacts/benchmark", "knowledge-evolution", "summary.json")
	}
	if *logRetrieval == "" {
		*logRetrieval = latestArtifactFile("artifacts/benchmark", "log-retrieval", "log_retrieval_report.json")
	}
	if *incidentRetrieval == "" {
		*incidentRetrieval = latestArtifactFile("artifacts/benchmark", "incident-retrieval", "incident_retrieval_report.json")
	}
	if *output == "" {
		*output = filepath.Join(artifactlayout.RunDirectory("artifacts/benchmark", "autonomous", "report", time.Now().UTC()), "autonomous_sre_report.json")
	}
	load := func(path string) any {
		var value any
		if err := readJSON(path, &value); err != nil {
			fatal(err)
		}
		return value
	}
	diagnosisValue := load(*diagnosis)
	diagnosisCases := 0
	if summary, ok := diagnosisValue.(map[string]any); ok {
		if total, ok := summary["total"].(float64); ok {
			diagnosisCases = int(total)
		}
	}
	cases := diagnosisCases
	if cases > 50 || cases == 0 {
		cases = 50
	}
	fatal(benchmarkreports.WriteEnvelope(*output, benchmarkreports.Envelope{
		Benchmark: "autonomous_sre",
		Manifest:  runtimeManifest(),
		Dataset:   benchmarkreports.DatasetInfo{Name: "resolved-incidents", Size: cases},
		Metrics: map[string]any{
			"log_retrieval":       load(*logRetrieval),
			"incident_retrieval":  load(*incidentRetrieval),
			"diagnosis":           diagnosisValue,
			"agent_behavior":      load(*agent),
			"recovery":            load(*recovery),
			"knowledge_evolution": load(*knowledge),
		},
		Cases:       cases,
		Limitations: []string{"This report composes the full live diagnosis/recovery run and the independent retrieval and knowledge-evolution suites; evaluator-only labels remain outside Agent context."},
	}))
	fmt.Printf("suite=autonomous_sre output=%s\n", *output)
}

func loadCaseResults(path string) []reporter.CaseResult {
	items, err := readCaseResults(path)
	fatal(err)
	return items
}

func runtimeManifest() any {
	var value any
	if err := readJSON("artifacts/benchmark/manifest/runtime.json", &value); err != nil {
		return nil
	}
	return value
}

func newBenchmarkReranker() rerankerclient.Service {
	enabled := strings.EqualFold(env("RERANKER_ENABLED", "false"), "true")
	if !enabled {
		return nil
	}
	cfg := config.RerankerConfig{Enabled: true, Protocol: env("RERANKER_PROTOCOL", "openai-compatible"), BaseURL: os.Getenv("RERANKER_BASE_URL"), APIPath: env("RERANKER_API_PATH", "/reranks"), APIKey: os.Getenv("RERANKER_API_KEY"), Model: os.Getenv("RERANKER_MODEL"), Timeout: envDuration("RERANKER_TIMEOUT", 30*time.Second), MaxRetries: envInt("RERANKER_MAX_RETRIES", 1), MaxDocumentBytes: envInt("RERANKER_MAX_DOCUMENT_BYTES", 8192), MaxPayloadBytes: envInt("RERANKER_MAX_PAYLOAD_BYTES", 1048576)}
	fatal(config.ValidateReranker(cfg))
	return rerankerclient.New(cfg)
}

func writeSuiteManifest(path string, payload map[string]any) error {
	b, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o640)
}

func runSuiteReport(args []string) {
	fs := flag.NewFlagSet("suite-report", flag.ExitOnError)
	manifestPath := fs.String("manifest", "benchmark/manifests/autonomous.yaml", "benchmark manifest")
	root := fs.String("root", "artifacts/benchmark", "benchmark artifact root")
	output := fs.String("output", "", "final report directory; defaults to a timestamped autonomous report directory")
	_ = fs.Parse(args)
	if *output == "" {
		*output = artifactlayout.RunDirectory(*root, "autonomous", "report", time.Now().UTC())
	}
	manifest, _, err := benchmarkmanifests.Load(*manifestPath)
	fatal(err)
	load := func(path string) any {
		var value any
		if err := readJSON(path, &value); err != nil {
			return nil
		}
		return value
	}
	report := benchmarkreports.SuiteReport{Manifest: manifest, GeneratedAt: time.Now().UTC(),
		LogRetrieval:       load(latestArtifactFile(*root, "log-retrieval", "log_retrieval_report.json")),
		IncidentRetrieval:  load(latestArtifactFile(*root, "incident-retrieval", "incident_retrieval_report.json")),
		Diagnosis:          load(latestSummary(*root)),
		Recovery:           load(latestArtifactFile(*root, "recovery", "recovery_report.json")),
		AgentBehavior:      load(latestArtifactFile(*root, "agent", "agent_behavior_report.json")),
		KnowledgeEvolution: load(latestArtifactFile(*root, "knowledge-evolution", "summary.json")),
		Correlation:        load(latestArtifactFile(*root, "correlation", "correlation-summary.json")),
		Autonomous:         load(latestArtifactFile(*root, "autonomous", "autonomous_sre_report.json")),
		Limitations:        []string{"Recovery and Agent Behavior are measured in the public standard run envelope."},
	}
	fatal(benchmarkreports.WriteSuite(*output, report))
	fmt.Printf("suite=report output=%s\n", *output)
}

func latestSummary(root string) string {
	var newest string
	var newestTime time.Time
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() || filepath.Base(path) != "summary.json" {
			return nil
		}
		var value map[string]any
		if err := readJSON(path, &value); err != nil {
			return nil
		}
		// Diagnosis summaries have a persisted total and RCA metric. Other
		// suites also use summary.json but must never be mislabelled as diagnosis.
		if _, ok := value["total"]; !ok {
			return nil
		}
		if _, ok := value["root_cause_accuracy"]; !ok {
			return nil
		}
		info, err := entry.Info()
		if err == nil && (newest == "" || info.ModTime().After(newestTime)) {
			newest, newestTime = path, info.ModTime()
		}
		return nil
	})
	return newest
}

func latestArtifactFile(root, suite, filename string) string {
	var newest string
	var newestTime time.Time
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() || filepath.Base(path) != filename {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil
		}
		parts := strings.Split(filepath.ToSlash(rel), "/")
		foundSuite := false
		for _, part := range parts[:len(parts)-1] {
			if part == suite {
				foundSuite = true
				break
			}
		}
		if !foundSuite {
			return nil
		}
		info, err := entry.Info()
		if err == nil && (newest == "" || info.ModTime().After(newestTime)) {
			newest, newestTime = path, info.ModTime()
		}
		return nil
	})
	return newest
}
