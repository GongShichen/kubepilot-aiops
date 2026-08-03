package retrieval

import "context"

type Document struct {
	ID        string    `json:"id"`
	Service   string    `json:"service"`
	Namespace string    `json:"namespace"`
	Category  string    `json:"category"`
	Template  string    `json:"template"`
	RootCause string    `json:"root_cause"`
	Recovery  string    `json:"recovery"`
	Vector    []float32 `json:"-"`
}
type VectorStore interface {
	Upsert(context.Context, []Document) error
	Search(context.Context, []float32, map[string]string, int) ([]Document, error)
}
type HybridRetriever struct{ Store VectorStore }

func (h HybridRetriever) Search(ctx context.Context, vector []float32, service, namespace string, topK int) ([]Document, error) {
	return h.Store.Search(ctx, vector, map[string]string{"service": service, "namespace": namespace}, topK)
}
