package agent

import (
	"context"

	"github.com/kubepilot-aiops/kubepilot/internal/domain"
)

// HistoricalCandidateRetriever is the only retrieval contract exposed to an
// Agent. Its implementations are production adapters; the Agent still chooses
// which capability to invoke through Eino Tools.
type HistoricalCandidateRetriever interface {
	Semantic(context.Context, domain.IncidentFeatures, int) ([]domain.RetrievalCandidate, error)
	Lexical(context.Context, domain.IncidentFeatures, int) ([]domain.RetrievalCandidate, error)
	Topology(context.Context, domain.IncidentFeatures, int) ([]domain.RetrievalCandidate, error)
}

type CausalPatternReader interface {
	ListCausalPatterns(context.Context, string) ([]domain.CausalPattern, error)
}
