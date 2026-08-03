package retrieval

import (
	"context"

	llm "github.com/kubepilot-aiops/kubepilot/internal/model"
)

type EmbeddingPipeline struct{ Client *llm.Embedder }

func (p EmbeddingPipeline) EmbedTemplates(ctx context.Context, items []TemplateResult) ([][]float32, error) {
	texts := make([]string, len(items))
	for i, v := range items {
		texts[i] = v.Template
	}
	return p.Client.Embed(ctx, texts)
}
