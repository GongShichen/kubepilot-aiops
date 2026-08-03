package retrieval

import (
	"context"
	"strings"
	"time"

	"github.com/kubepilot-aiops/kubepilot/internal/domain"
	"github.com/oklog/ulid/v2"
)

type EmbeddingClient interface {
	Embed(context.Context, []string) ([][]float32, error)
}

type HistoricalEvidence struct {
	Embedder EmbeddingClient
	Store    VectorStore
	TopK     int
}

func (h HistoricalEvidence) Collect(ctx context.Context, in *domain.Incident) ([]domain.Evidence, error) {
	parts := []string{in.Summary, in.Service, in.Resource}
	for _, item := range in.Evidence {
		parts = append(parts, item.Summary)
	}
	vectors, err := h.Embedder.Embed(ctx, []string{strings.Join(parts, "\n")})
	if err != nil {
		return nil, err
	}
	topK := h.TopK
	if topK <= 0 {
		topK = 5
	}
	docs, err := h.Store.Search(ctx, vectors[0], map[string]string{"service": in.Service, "namespace": in.Namespace}, topK)
	if err != nil {
		return nil, err
	}
	out := make([]domain.Evidence, 0, len(docs))
	for _, doc := range docs {
		out = append(out, domain.Evidence{ID: ulid.Make().String(), Source: "milvus+embedding", Kind: "historical_incident", Summary: doc.RootCause, Data: map[string]any{"document_id": doc.ID, "category": doc.Category, "template": doc.Template, "recovery": doc.Recovery}, ObservedAt: time.Now().UTC()})
	}
	return out, nil
}
