package evaluator

// Result is the common, evaluator-side result envelope used by the
// benchmark suites. Metrics are intentionally opaque to the orchestrator so
// each benchmark can retain its own metric family without sharing scoring
// assumptions.
type Result struct {
	Benchmark string   `json:"benchmark"`
	Metrics   any      `json:"metrics,omitempty"`
	Errors    []string `json:"errors,omitempty"`
}

// UnifiedEvaluator is the common contract for all benchmark evaluators.
// Implementations receive an observation and evaluator-only expected data as
// separate values. Implementations must not mutate either input.
type UnifiedEvaluator interface {
	Evaluate(input, output any) Result
}
