package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/kubepilot-aiops/kubepilot/agent"
	"github.com/kubepilot-aiops/kubepilot/internal/api"
	"github.com/kubepilot-aiops/kubepilot/internal/causal"
	causaldiscovery "github.com/kubepilot-aiops/kubepilot/internal/causal/discovery"
	"github.com/kubepilot-aiops/kubepilot/internal/config"
	"github.com/kubepilot-aiops/kubepilot/internal/domain"
	"github.com/kubepilot-aiops/kubepilot/internal/memory"
	llm "github.com/kubepilot-aiops/kubepilot/internal/model"
	rankpolicy "github.com/kubepilot-aiops/kubepilot/internal/reasoning/evidence"
	rerankerclient "github.com/kubepilot-aiops/kubepilot/internal/retrieval/reranker"
	"github.com/kubepilot-aiops/kubepilot/internal/service"
	"github.com/kubepilot-aiops/kubepilot/internal/store"
	"github.com/kubepilot-aiops/kubepilot/internal/telemetry"
	"github.com/kubepilot-aiops/kubepilot/reasoning"
	"github.com/kubepilot-aiops/kubepilot/retrieval"
	"github.com/kubepilot-aiops/kubepilot/tools"
)

type unavailableExecutor struct{}

func (unavailableExecutor) DryRun(context.Context, *domain.RecoveryProposal) (*domain.DryRunResult, error) {
	return &domain.DryRunResult{Error: "kubernetes client unavailable", ValidatedAt: time.Now().UTC()}, errors.New("kubernetes client unavailable")
}

func (unavailableExecutor) Execute(context.Context, *domain.Incident, domain.RecoveryProposal) error {
	return errors.New("kubernetes client unavailable")
}
func (unavailableExecutor) Verify(context.Context, *domain.Incident) (domain.Verification, error) {
	return domain.Verification{}, errors.New("kubernetes client unavailable")
}

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	cfg, err := config.Load()
	if err != nil {
		slog.Error("configuration error", "error", err)
		os.Exit(1)
	}
	shutdownTelemetry, telemetryErr := telemetry.Init(ctx, "kubepilot-agent")
	if telemetryErr != nil {
		slog.Warn("OpenTelemetry disabled", "error", telemetryErr)
	} else {
		defer shutdownTelemetry(context.Background())
	}
	pg, err := store.NewPostgres(ctx, cfg.DatabaseURL)
	if err != nil {
		slog.Error("postgres unavailable", "error", err)
		os.Exit(1)
	}
	defer pg.Close()
	patternSeed, err := reasoning.LoadPatternSeed(cfg.Reasoning.CausalPatternFile)
	if err != nil {
		slog.Error("load causal pattern seed", "error", err)
		os.Exit(1)
	}
	if err = pg.SeedCausalPatterns(ctx, patternSeed.Patterns); err != nil {
		slog.Error("seed causal patterns", "error", err)
		os.Exit(1)
	}
	causalMatcher := causal.DefaultMatcher()
	if paths, globErr := filepath.Glob(filepath.Join(cfg.Reasoning.CausalPatternDirectory, "*.yaml")); globErr == nil && len(paths) > 0 {
		if loaded, loadErr := causal.Load(paths...); loadErr == nil {
			causalMatcher = loaded
		} else {
			slog.Warn("load topology causal patterns failed; using built-in patterns", "error", loadErr)
		}
	}
	redisStore, err := store.NewRedis(cfg.RedisURL)
	if err != nil {
		slog.Error("redis configuration error", "error", err)
		os.Exit(1)
	}
	defer redisStore.Close()
	if err = redisStore.Ping(ctx); err != nil {
		slog.Error("redis unavailable", "error", err)
		os.Exit(1)
	}
	chat := llm.NewHotClient(cfg.Chat, cfg.ConfigEnvFile, cfg.ConfigReloadEvery, cfg.ConfigRetryEvery)
	go chat.Run(ctx)
	loki := tools.NewLoki(cfg.LokiURL)
	collectors := map[string]agent.Collector{
		"metric": agent.MetricCollector{Client: tools.NewPrometheus(cfg.PrometheusURL)},
		"log":    agent.LogCollector{Loki: loki},
		"trace":  agent.TraceCollector{Client: tools.NewJaeger(cfg.JaegerURL)},
	}
	if cfg.BusinessProbeURL != "" {
		collectors["business"] = agent.BusinessProbeCollector{URL: cfg.BusinessProbeURL}
	}
	var executor agent.Executor = unavailableExecutor{}
	if kube, err := tools.NewKubernetes(cfg.Kubeconfig, cfg.AllowedNamespaces); err == nil {
		collectors["kubernetes"] = agent.KubernetesEvidenceCollector{Client: kube}
		executor = agent.KubernetesExecutor{Client: kube, Guard: redisStore}
	} else {
		slog.Warn("kubernetes collector disabled", "error", err)
	}
	historical := &retrieval.IncidentRetrievalEngine{HistoricalRetriever: retrieval.HistoricalRetriever{Knowledge: pg}}
	var learnerEmbedder retrieval.EmbeddingClient
	var learnerVectors retrieval.VectorStore
	if cfg.ValidateEmbedding() == nil {
		embedder := llm.NewEmbedder(cfg.Embedding)
		milvus := retrieval.NewMilvusStore(cfg.MilvusAddress, cfg.HistoryCollection, cfg.Embedding.Dimensions)
		if ensureErr := milvus.Ensure(ctx); ensureErr != nil {
			slog.Warn("historical retrieval disabled", "error", ensureErr)
		} else {
			historical.Embedder, historical.Vectors = embedder, milvus
			learnerEmbedder, learnerVectors = embedder, milvus
		}
		logIndex := retrieval.NewMilvusStore(cfg.MilvusAddress, cfg.LogIndexCollection, cfg.Embedding.Dimensions)
		if ensureErr := logIndex.Ensure(ctx); ensureErr != nil {
			slog.Warn("indexed log retrieval disabled", "error", ensureErr)
		} else {
			collectors["log"] = agent.LogCollector{Loki: loki, Indexed: retrieval.IndexedLogRetriever{Embedder: embedder, Store: logIndex, Cursors: redisStore, TopK: 5}}
		}
	}
	agents, err := agent.NewAgentRegistry(ctx, chat)
	if err != nil {
		slog.Error("register Eino ADK agents", "error", err)
		os.Exit(1)
	}
	agents.ConfigureRuntimePolicy(agent.RuntimePolicy{
		Supervisor:               domain.AgentBudget{MaxIterations: cfg.AgentBudgets.Supervisor.MaxIterations, MaxToolUses: cfg.AgentBudgets.Supervisor.MaxToolUses, MaxTokens: cfg.AgentBudgets.Supervisor.MaxTokens, MaxCorrections: cfg.AgentBudgets.Supervisor.MaxCorrections},
		Diagnosis:                domain.AgentBudget{MaxIterations: cfg.AgentBudgets.Diagnosis.MaxIterations, MaxToolUses: cfg.AgentBudgets.Diagnosis.MaxToolUses, MaxTokens: cfg.AgentBudgets.Diagnosis.MaxTokens, MaxCorrections: cfg.AgentBudgets.Diagnosis.MaxCorrections},
		Recovery:                 domain.AgentBudget{MaxIterations: cfg.AgentBudgets.Recovery.MaxIterations, MaxToolUses: cfg.AgentBudgets.Recovery.MaxToolUses, MaxTokens: cfg.AgentBudgets.Recovery.MaxTokens, MaxCorrections: cfg.AgentBudgets.Recovery.MaxCorrections},
		RequestMaxTokens:         cfg.Chat.MaxTokens,
		ModelMaxRetries:          cfg.Chat.MaxRetries,
		InputPricePerMillion:     cfg.Chat.InputPricePerMillion,
		OutputPricePerMillion:    cfg.Chat.OutputPricePerMillion,
		ReasoningPricePerMillion: cfg.Chat.ReasoningPricePerMillion,
	})
	if err = agents.LoadToolCosts(cfg.Reasoning.ToolCostFile); err != nil {
		slog.Error("load Agent tool-cost policy", "error", err)
		os.Exit(1)
	}
	correlator, err := service.NewEinoCorrelator(ctx, pg, agents)
	if err != nil {
		slog.Error("register Eino correlation tools", "error", err)
		os.Exit(1)
	}
	checkpointStore := store.EinoCheckpointStore{Redis: redisStore, TTL: 7 * 24 * time.Hour}
	rankingPolicy, err := rankpolicy.LoadPolicy(cfg.Reasoning.RankingPolicyFile)
	if err != nil {
		slog.Error("load ranking policy", "error", err)
		os.Exit(1)
	}
	reasoningEngine := reasoning.New(reasoning.Config{SemanticTopK: cfg.Reasoning.SemanticTopK, LexicalTopK: cfg.Reasoning.LexicalTopK, TopologyTopK: cfg.Reasoning.TopologyTopK, RRFK: cfg.Reasoning.RRFK, RerankTopK: cfg.Reasoning.RerankTopK, ModelEvidenceMaxItems: cfg.Reasoning.ModelEvidenceMaxItems, ModelContextMaxBytes: cfg.Reasoning.ModelContextMaxBytes, RankingPolicy: &rankingPolicy})
	hotReranker := rerankerclient.NewHotClient(cfg.Reranker, cfg.ConfigEnvFile, cfg.ConfigReloadEvery, cfg.ConfigRetryEvery)
	if probeErr := hotReranker.Probe(ctx); probeErr != nil {
		slog.Warn("reranker configuration probe failed", "error", probeErr)
	}
	var neuralReranker rerankerclient.Service = hotReranker
	go hotReranker.Run(ctx)
	historical.Reranker = neuralReranker
	topologyPatterns := store.NewPostgresTopologyPatternStore(pg)
	causalPatterns := store.NewPostgresCausalKnowledgeStore(pg)
	discoveredCandidates := store.NewPostgresCausalCandidateStore(pg)
	discoveryEngine := causaldiscovery.NewEngine(discoveredCandidates, causalPatterns)
	discoveryEngine.Patterns = causalPatterns
	discoveryEngine.Explainer = causaldiscovery.NewChatExplainer(chat)
	learner := service.CausalLearner{Store: pg, ConfidenceThreshold: cfg.Reasoning.CausalAutoActivateConfidence, Namespaces: cfg.Reasoning.CausalLearningNamespaces, EmbeddingVersion: cfg.Embedding.Model, Embedder: learnerEmbedder, Vectors: learnerVectors, TopologyPatterns: topologyPatterns, CausalPatterns: causalPatterns, Discovery: discoveryEngine, IncidentHistory: pg}
	agentMemory := &memory.Service{Historical: historical, Causal: pg, Reasoning: reasoningEngine, Recorder: pg, Writer: learner, Procedures: agents.ProceduralMemories()}
	supervisor, err := agent.NewSupervisor(ctx, agent.SupervisorDeps{Collectors: collectors, HistoricalCandidates: historical, Knowledge: pg, Reasoning: reasoningEngine, Agents: agents, Executor: executor, Checkpoints: checkpointStore, Reranker: neuralReranker, RankingPolicy: &rankingPolicy, Causal: causalMatcher, GraphStore: store.NewPostgresGraphStore(pg), TopologyPatterns: topologyPatterns, CausalPatterns: causalPatterns, DiscoveredPatterns: discoveredCandidates, Memory: agentMemory})
	if err != nil {
		slog.Error("compile Eino graph", "error", err)
		os.Exit(1)
	}
	manager := &service.IncidentManager{Store: pg, Supervisor: supervisor, Executor: executor, Hub: service.NewHub(), ModelSnapshotter: chat, Checkpoints: checkpointStore, AllowedNamespaces: cfg.AllowedNamespaces, CorrelationFallback: correlator, Learner: learner}
	supervisor.SetEventSink(manager.ObserveWorkflowEvent)
	benchmarkManager := service.NewBenchmarkManager("/usr/local/bin/kubepilot-benchmark", "http://127.0.0.1"+cfg.HTTPAddr, cfg.APIToken, cfg.WebhookToken, cfg.Kubeconfig, "artifacts/benchmark", manager.Hub, pg)
	srvAPI := &api.Server{Manager: manager, Benchmarks: benchmarkManager, Knowledge: pg, APIToken: cfg.APIToken, WebhookToken: cfg.WebhookToken, Readiness: func(readinessCtx context.Context) map[string]string {
		components := map[string]string{"postgres": "ready", "redis": "ready", "agent_registry": "ready"}
		if pingErr := pg.Ping(readinessCtx); pingErr != nil {
			components["postgres"] = "not_ready"
		}
		if pingErr := redisStore.Ping(readinessCtx); pingErr != nil {
			components["redis"] = "not_ready"
		}
		return components
	}, BenchmarkReadiness: func(readinessCtx context.Context) map[string]string {
		lokiStatus := endpointReadiness(readinessCtx, strings.TrimRight(cfg.LokiURL, "/")+"/ready")
		if lokiStatus == "ready" {
			if probeErr := loki.ProbeIngestion(readinessCtx); probeErr != nil {
				lokiStatus = "not_ready"
			}
		}
		components := map[string]string{
			"prometheus": endpointReadiness(readinessCtx, strings.TrimRight(cfg.PrometheusURL, "/")+"/-/ready"),
			"loki":       lokiStatus,
			"jaeger":     endpointReadiness(readinessCtx, strings.TrimRight(cfg.JaegerURL, "/")+"/api/services"),
			"kubernetes": "not_ready",
			"embedding":  "not_ready",
		}
		if collectors["kubernetes"] != nil {
			components["kubernetes"] = "ready"
		}
		if learnerEmbedder != nil && learnerVectors != nil {
			vectors, embedErr := learnerEmbedder.Embed(readinessCtx, []string{"kubepilot runtime readiness"})
			if embedErr == nil && len(vectors) == 1 && len(vectors[0]) == cfg.Embedding.Dimensions {
				components["embedding"] = "ready"
			}
		}
		if cfg.Reranker.Enabled {
			if probeErr := neuralReranker.Probe(readinessCtx); probeErr != nil {
				components["reranker"] = "not_ready"
			} else {
				components["reranker"] = "ready"
			}
		} else {
			components["reranker"] = "disabled"
		}
		if probeErr := chat.Probe(readinessCtx); probeErr != nil {
			components["model"] = "not_ready"
		} else {
			components["model"] = "ready"
		}
		return components
	}, ModelHealth: chat.Health, ModelProbe: func(c *gin.Context) error {
		return chat.Probe(c)
	}}
	if neuralReranker != nil {
		srvAPI.RerankerHealth = neuralReranker.Health
		srvAPI.RerankerProbe = func(c *gin.Context) error { return neuralReranker.Probe(c) }
	}
	server := &http.Server{Addr: cfg.HTTPAddr, Handler: srvAPI.Router(), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		slog.Info("KubePilot listening", "addr", cfg.HTTPAddr)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("server failed", "error", err)
			cancel()
		}
	}()
	<-ctx.Done()
	shutdownCtx, done := context.WithTimeout(context.Background(), 10*time.Second)
	defer done()
	_ = server.Shutdown(shutdownCtx)
}

func endpointReadiness(ctx context.Context, endpoint string) string {
	if strings.TrimSpace(endpoint) == "" {
		return "not_ready"
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "not_ready"
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return "not_ready"
	}
	response.Body.Close()
	if response.StatusCode/100 != 2 {
		return "not_ready"
	}
	return "ready"
}
