package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kubepilot-aiops/kubepilot/benchmark/evaluator"
	incidentretrieval "github.com/kubepilot-aiops/kubepilot/benchmark/incident_retrieval"
	logretrieval "github.com/kubepilot-aiops/kubepilot/benchmark/log_retrieval"
	benchmarkmanifests "github.com/kubepilot-aiops/kubepilot/benchmark/manifests"
	recoverybench "github.com/kubepilot-aiops/kubepilot/benchmark/recovery"
	"github.com/kubepilot-aiops/kubepilot/benchmark/reporter"
	benchmarkreports "github.com/kubepilot-aiops/kubepilot/benchmark/reports"
	artifactlayout "github.com/kubepilot-aiops/kubepilot/internal/artifacts"
	"github.com/kubepilot-aiops/kubepilot/internal/config"
	"github.com/kubepilot-aiops/kubepilot/internal/domain"
	llm "github.com/kubepilot-aiops/kubepilot/internal/model"
	rerankerclient "github.com/kubepilot-aiops/kubepilot/internal/retrieval/reranker"
	"github.com/kubepilot-aiops/kubepilot/internal/store"
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
	fatal(executeIncidentRetrieval(context.Background(), *dataset, *count, *output))
}

func executeIncidentRetrieval(ctx context.Context, datasetPath string, count int, output string) (runErr error) {
	dataset, err := incidentretrieval.LoadExpanded(datasetPath, count)
	if err != nil {
		return err
	}
	runID := time.Now().UTC().Format("20060102T150405000000000Z")
	dataset = dataset.Isolate(runID)
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if err = cfg.ValidateEmbedding(); err != nil {
		return err
	}
	postgres, err := store.NewPostgres(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer postgres.Close()
	collection := "kubepilot_evaluation_" + runID
	vectors := retrieval.NewMilvusStore(cfg.MilvusAddress, collection, cfg.Embedding.Dimensions)
	if err = vectors.Ensure(ctx); err != nil {
		return err
	}
	ids := dataset.IncidentIDs()
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		runErr = errors.Join(runErr,
			postgres.DeleteIncidents(cleanupCtx, ids),
			vectors.Drop(cleanupCtx),
		)
	}()
	candidates := dataset.Candidates()
	createdAt := time.Now().UTC()
	for _, candidate := range candidates {
		incident := &domain.Incident{
			ID: candidate.IncidentID, Status: domain.StatusResolved,
			Namespace: candidate.Namespace, Service: candidate.Service, Resource: candidate.Resource,
			Summary: candidate.Summary, RootCause: candidate.RootCause,
			RootCauseCategory: candidate.Category, CreatedAt: createdAt, UpdatedAt: createdAt,
		}
		if err = postgres.Create(ctx, incident); err != nil {
			return fmt.Errorf("create isolated incident knowledge source: %w", err)
		}
		if err = postgres.UpsertIncidentKnowledge(ctx, incident, candidate.Features, cfg.Embedding.Model); err != nil {
			return fmt.Errorf("index isolated lexical incident knowledge: %w", err)
		}
	}
	embedder := llm.NewEmbedder(cfg.Embedding)
	texts := make([]string, len(candidates))
	for index, candidate := range candidates {
		texts[index] = retrieval.StructuredIncidentDocument(candidate)
	}
	embeddings, err := embedder.Embed(ctx, texts)
	if err != nil {
		return fmt.Errorf("embed isolated incident corpus: %w", err)
	}
	if len(embeddings) != len(candidates) {
		return fmt.Errorf("embedding count %d, expected %d", len(embeddings), len(candidates))
	}
	documents := make([]retrieval.Document, len(candidates))
	for index, candidate := range candidates {
		documents[index] = retrieval.Document{
			ID: candidate.IncidentID, Namespace: candidate.Namespace, Service: candidate.Service,
			Category: candidate.Category, Template: candidate.Summary, RootCause: candidate.RootCause,
			Vector: embeddings[index],
		}
	}
	for start := 0; start < len(documents); start += 100 {
		end := min(start+100, len(documents))
		if err = vectors.Upsert(ctx, documents[start:end]); err != nil {
			return fmt.Errorf("index isolated semantic incident knowledge: %w", err)
		}
	}
	var neural rerankerclient.Service
	if cfg.Reranker.Enabled {
		neural = rerankerclient.New(cfg.Reranker)
	}
	engine := &retrieval.IncidentRetrievalEngine{
		HistoricalRetriever: retrieval.HistoricalRetriever{Embedder: embedder, Vectors: vectors, Knowledge: postgres},
		Reranker:            neural,
	}
	report, err := incidentretrieval.Run(ctx, incidentretrieval.RunnerConfig{
		Dataset: dataset, Engine: engine, OutputDir: output,
		Progress: func(current, total int) { fmt.Printf("suite=incident_retrieval progress=%d/%d\n", current, total) },
	})
	if err != nil {
		return err
	}
	if err = writeSuiteManifest(filepath.Join(output, "manifest.json"), map[string]any{"suite": "incident_retrieval", "dataset": datasetPath, "queries": report.Queries(), "strategies": len(report.Strategies), "category_counts": report.CategoryCounts, "git_commit": gitCommit()}); err != nil {
		return err
	}
	if err = benchmarkreports.WriteEnvelope(filepath.Join(output, "benchmark_report.json"), benchmarkreports.Envelope{
		Benchmark: "incident_retrieval",
		Manifest:  runtimeManifest(),
		Dataset:   benchmarkreports.DatasetInfo{Name: "incident-retrieval", Size: report.Queries(), CategoryCounts: report.CategoryCounts},
		Metrics:   report,
		Cases:     report.Queries(),
	}); err != nil {
		return err
	}
	fmt.Printf("suite=incident_retrieval queries=%d strategies=%d output=%s\n", report.Queries(), len(report.Strategies), output)
	return nil
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
	metrics := evaluator.EvaluateAgentCaseResults(items)
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
	ablation := fs.String("ablation", "", "causal ablation report")
	logRetrieval := fs.String("log-retrieval", "", "log retrieval report")
	incidentRetrieval := fs.String("incident-retrieval", "", "incident retrieval report")
	output := fs.String("output", "", "report path; defaults to a timestamped autonomous directory")
	_ = fs.Parse(args)
	for name, path := range map[string]string{"diagnosis": *diagnosis, "agent": *agent, "recovery": *recovery, "knowledge": *knowledge, "ablation": *ablation, "log-retrieval": *logRetrieval, "incident-retrieval": *incidentRetrieval} {
		if strings.TrimSpace(path) == "" {
			fatal(fmt.Errorf("--%s is required; reports never guess the newest artifact", name))
		}
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
			"causal_ablation":     load(*ablation),
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
	manifestPath := fs.String("manifest", "benchmark/manifests/default.yaml", "benchmark manifest")
	root := fs.String("root", "artifacts/benchmark", "benchmark artifact root")
	diagnosis := fs.String("diagnosis", "", "exact diagnosis summary or comparison artifact")
	recovery := fs.String("recovery", "", "exact recovery report")
	agent := fs.String("agent", "", "exact agent behavior report")
	knowledge := fs.String("knowledge", "", "exact knowledge report")
	ablation := fs.String("ablation", "", "exact causal ablation report")
	correlation := fs.String("correlation", "", "exact correlation report")
	autonomous := fs.String("autonomous", "", "exact autonomous report")
	logRetrieval := fs.String("log-retrieval", "", "exact log retrieval report")
	incidentRetrieval := fs.String("incident-retrieval", "", "exact incident retrieval report")
	output := fs.String("output", "", "final report directory; defaults to a timestamped autonomous report directory")
	_ = fs.Parse(args)
	paths := map[string]string{"diagnosis": *diagnosis, "recovery": *recovery, "agent": *agent, "knowledge": *knowledge, "ablation": *ablation, "correlation": *correlation, "autonomous": *autonomous, "log-retrieval": *logRetrieval, "incident-retrieval": *incidentRetrieval}
	for name, path := range paths {
		if strings.TrimSpace(path) == "" {
			fatal(fmt.Errorf("--%s is required; suite reports must reference one comparison run explicitly", name))
		}
	}
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
		LogRetrieval:       load(*logRetrieval),
		IncidentRetrieval:  load(*incidentRetrieval),
		Diagnosis:          load(*diagnosis),
		Recovery:           load(*recovery),
		AgentBehavior:      load(*agent),
		KnowledgeEvolution: load(*knowledge),
		Ablation:           load(*ablation),
		Correlation:        load(*correlation),
		Autonomous:         load(*autonomous),
		Limitations:        []string{"Recovery and Agent Behavior are measured in the public standard run envelope."},
	}
	fatal(benchmarkreports.WriteSuite(*output, report))
	fmt.Printf("suite=report output=%s\n", *output)
}
