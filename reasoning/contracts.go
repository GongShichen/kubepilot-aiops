package reasoning

import "github.com/kubepilot-aiops/kubepilot/internal/domain"

// CandidateReranker is the stable extension boundary after multi-retriever
// fusion. The production implementation is deliberately deterministic and
// explainable; a future cross-encoder can implement this contract without
// changing the Incident Graph or retrieval ToolsNode boundaries.
type CandidateReranker interface {
	Rerank(domain.IncidentFeatures, []domain.RetrievalCandidate) []domain.RetrievalCandidate
}

var _ CandidateReranker = (*Engine)(nil)
