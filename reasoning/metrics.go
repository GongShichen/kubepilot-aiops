package reasoning

import "github.com/prometheus/client_golang/prometheus"

var (
	evidenceContextItems    = prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "kubepilot_reasoning_evidence_items", Help: "Evidence items before and after deterministic context ranking.", Buckets: []float64{1, 2, 4, 8, 12, 24, 48, 96}}, []string{"stage"})
	evidenceContextBytes    = prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "kubepilot_reasoning_evidence_bytes", Help: "Evidence payload bytes before and after deterministic context ranking.", Buckets: prometheus.ExponentialBuckets(1024, 2, 8)}, []string{"stage"})
	retrievalCandidateCount = prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "kubepilot_reasoning_retrieval_candidates", Help: "Historical candidates emitted by each retriever and fusion.", Buckets: []float64{0, 1, 5, 10, 30, 50, 100}}, []string{"source"})
	rankingScore            = prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "kubepilot_reasoning_ranking_score", Help: "Deterministic reasoning component scores.", Buckets: prometheus.LinearBuckets(0, .1, 11)}, []string{"component"})
)

func init() {
	prometheus.MustRegister(evidenceContextItems, evidenceContextBytes, retrievalCandidateCount, rankingScore)
}
