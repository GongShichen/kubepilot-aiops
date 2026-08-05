package evolution

import (
	"github.com/kubepilot-aiops/kubepilot/benchmark/causalevolution"
	"github.com/kubepilot-aiops/kubepilot/benchmark/topologyevolution"
)

// SuiteReport keeps topology and causal knowledge evolution metrics together
// while retaining their distinct resolved-Incident datasets and evaluators.
type SuiteReport struct {
	Topology topologyevolution.Metrics `json:"topology"`
	Causal   causalevolution.Metrics   `json:"causal"`
}

func EvaluateSuite(topologyCases []topologyevolution.Case, causalCases []causalevolution.Case) SuiteReport {
	return SuiteReport{Topology: topologyevolution.Evaluate(topologyCases), Causal: causalevolution.Evaluate(causalCases)}
}
