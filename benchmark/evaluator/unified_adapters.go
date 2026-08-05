package evaluator

import "fmt"

// LogRetrievalEvaluator adapts log/template observations to the common
// contract without importing incident or reasoning data.
type LogRetrievalEvaluator struct{}

func (LogRetrievalEvaluator) Evaluate(input, output any) Result {
	return Result{Benchmark: "log_retrieval", Metrics: map[string]any{"input_type": typeName(input), "output_type": typeName(output)}}
}

// IncidentRetrievalEvaluator is the common-contract adapter for historical
// incident ranking. Metric computation remains in benchmark/incident_retrieval.
type IncidentRetrievalEvaluator struct{}

func (IncidentRetrievalEvaluator) Evaluate(input, output any) Result {
	return Result{Benchmark: "incident_retrieval", Metrics: map[string]any{"input_type": typeName(input), "output_type": typeName(output)}}
}

type RecoverySuiteEvaluator struct{}

func (RecoverySuiteEvaluator) Evaluate(input, output any) Result {
	return Result{Benchmark: "recovery", Metrics: map[string]any{"input_type": typeName(input), "output_type": typeName(output)}}
}

type EvolutionSuiteEvaluator struct{}

func (EvolutionSuiteEvaluator) Evaluate(input, output any) Result {
	return Result{Benchmark: "knowledge_evolution", Metrics: map[string]any{"input_type": typeName(input), "output_type": typeName(output)}}
}

func typeName(value any) string {
	if value == nil {
		return "nil"
	}
	return fmt.Sprintf("%T", value)
}
