package discovery

import (
	"context"

	"github.com/kubepilot-aiops/kubepilot/internal/domain"
)

type Extractor struct{}

func NewExtractor() Extractor { return Extractor{} }

func (Extractor) Extract(_ context.Context, in *domain.Incident) (IncidentCausalGraph, error) {
	return BuildIncidentCausalGraph(in)
}

func (e Extractor) ExtractMany(ctx context.Context, incidents []*domain.Incident) ([]IncidentCausalGraph, error) {
	graphs := make([]IncidentCausalGraph, 0, len(incidents))
	for _, incident := range incidents {
		graph, err := e.Extract(ctx, incident)
		if err != nil {
			continue
		}
		graphs = append(graphs, graph)
	}
	return graphs, nil
}
