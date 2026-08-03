package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/kubepilot-aiops/kubepilot/internal/config"
	llm "github.com/kubepilot-aiops/kubepilot/internal/model"
	"github.com/kubepilot-aiops/kubepilot/internal/store"
	"github.com/kubepilot-aiops/kubepilot/retrieval"
	"github.com/kubepilot-aiops/kubepilot/tools"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	cfg, err := config.Load()
	if err != nil {
		slog.Error("configuration error", "error", err)
		os.Exit(1)
	}
	if err = cfg.ValidateEmbedding(); err != nil {
		slog.Error("embedding configuration error", "error", err)
		os.Exit(1)
	}
	postgres, err := store.NewPostgres(ctx, cfg.DatabaseURL)
	if err != nil {
		slog.Error("postgres unavailable", "error", err)
		os.Exit(1)
	}
	defer postgres.Close()
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
	milvus := retrieval.NewMilvusStore(cfg.MilvusAddress, cfg.LogIndexCollection, cfg.Embedding.Dimensions)
	if err = milvus.Ensure(ctx); err != nil {
		slog.Error("log index unavailable", "error", err)
		os.Exit(1)
	}
	parser := retrieval.NewWSParser(cfg.Drain3URL, cfg.Drain3Token)
	defer parser.Close()
	indexer := &retrieval.LogIndexer{Loki: tools.NewLoki(cfg.LokiURL), Parser: parser, Embedder: llm.NewEmbedder(cfg.Embedding), Store: milvus, Metadata: postgres, Cursors: redisStore, Namespaces: cfg.AllowedNamespaces, PollEvery: cfg.LogIndexerInterval}
	if err = indexer.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		slog.Error("log indexer stopped", "error", err)
		os.Exit(1)
	}
}
