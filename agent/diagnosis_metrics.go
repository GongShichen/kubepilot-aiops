package agent

import "github.com/prometheus/client_golang/prometheus"

var (
	workerRequestDuplicate = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "worker_request_duplicate_total",
		Help: "Supplemental worker requests skipped because their server fingerprint was already executed.",
	})
	debateWithoutNewEvidence = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "debate_round_without_new_evidence_total",
		Help: "Supplemental evidence rounds that produced no new logical evidence IDs.",
	})
	arbitrationGateFailure = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "arbitration_gate_failure_total",
		Help: "Deterministic arbitration gate failures by gate.",
	}, []string{"gate"})
)

func init() {
	prometheus.MustRegister(workerRequestDuplicate, debateWithoutNewEvidence, arbitrationGateFailure)
}
