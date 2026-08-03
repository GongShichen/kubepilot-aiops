package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/kubepilot-aiops/kubepilot/agent"
	"github.com/kubepilot-aiops/kubepilot/internal/api"
	"github.com/kubepilot-aiops/kubepilot/internal/config"
	"github.com/kubepilot-aiops/kubepilot/internal/domain"
	llm "github.com/kubepilot-aiops/kubepilot/internal/model"
	"github.com/kubepilot-aiops/kubepilot/internal/service"
	"github.com/kubepilot-aiops/kubepilot/internal/store"
	"github.com/kubepilot-aiops/kubepilot/internal/telemetry"
	"github.com/kubepilot-aiops/kubepilot/retrieval"
	"github.com/kubepilot-aiops/kubepilot/tools"
)

type unavailableExecutor struct{}

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
	parser := retrieval.NewWSParser(cfg.Drain3URL, cfg.Drain3Token)
	defer parser.Close()
	collectors := map[string]agent.Collector{"metric": agent.MetricAgent{Client: tools.NewPrometheus(cfg.PrometheusURL)}, "log": agent.LogAgent{Loki: tools.NewLoki(cfg.LokiURL), Parser: parser}, "trace": agent.TraceAgent{Client: tools.NewJaeger(cfg.JaegerURL)}}
	var executor agent.Executor = unavailableExecutor{}
	if kube, err := tools.NewKubernetes(cfg.Kubeconfig, cfg.AllowedNamespaces); err == nil {
		collectors["kubernetes"] = agent.KubernetesEvidenceAgent{Client: kube}
		executor = agent.KubernetesExecutor{Client: kube}
	} else {
		slog.Warn("kubernetes collector disabled", "error", err)
	}
	diagnosis := &agent.DiagnosisAgent{Model: chat}
	recovery := &agent.RecoveryAgent{Model: chat}
	var historical agent.Collector
	if cfg.ValidateEmbedding() == nil {
		milvus := retrieval.NewMilvusStore(cfg.MilvusAddress, cfg.HistoryCollection, cfg.Embedding.Dimensions)
		if ensureErr := milvus.Ensure(ctx); ensureErr != nil {
			slog.Warn("historical retrieval disabled", "error", ensureErr)
		} else {
			historical = retrieval.HistoricalEvidence{Embedder: llm.NewEmbedder(cfg.Embedding), Store: milvus, TopK: 5}
		}
	}
	supervisor, err := agent.NewSupervisor(ctx, agent.SupervisorDeps{Collectors: collectors, Historical: historical, Diagnosis: diagnosis, Recovery: recovery})
	if err != nil {
		slog.Error("compile Eino graph", "error", err)
		os.Exit(1)
	}
	manager := &service.IncidentManager{Store: pg, Supervisor: supervisor, Executor: executor, Hub: service.NewHub(), ModelSnapshotter: chat}
	benchmarkManager := service.NewBenchmarkManager("/usr/local/bin/kubepilot-benchmark", "http://127.0.0.1"+cfg.HTTPAddr, cfg.APIToken, cfg.WebhookToken, cfg.Kubeconfig, "artifacts/benchmark", manager.Hub)
	srvAPI := &api.Server{Manager: manager, Benchmarks: benchmarkManager, APIToken: cfg.APIToken, WebhookToken: cfg.WebhookToken, ModelHealth: chat.Health, ModelProbe: func(c *gin.Context) error {
		return chat.Probe(c)
	}}
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
