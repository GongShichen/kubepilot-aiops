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

// BrainHybridRetriever is the only historical-context boundary available to
// KubePilot. Baseline retrievers remain independent and are not a fallback for
// the Brain path.
type BrainHybridRetriever interface {
	Retrieve(context.Context, domain.HybridRetrievalQuery) (domain.HybridRetrievalResult, error)
}

type BrainSkillRetriever interface {
	Search(context.Context, domain.SkillRetrievalQuery) (domain.SkillRetrievalResult, error)
}

type CausalPatternReader interface {
	ListCausalPatterns(context.Context, string) ([]domain.CausalPattern, error)
}

type MemoryService interface {
	Read(context.Context, domain.MemoryQuery) ([]domain.MemoryResult, error)
	WriteVerifiedIncident(context.Context, domain.IncidentLearningInput) error
	RecordAccess(context.Context, domain.MemoryAccessEvent) error
}
