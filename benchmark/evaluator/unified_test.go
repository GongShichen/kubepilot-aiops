package evaluator

import "testing"

type testUnifiedEvaluator struct{}

func (testUnifiedEvaluator) Evaluate(input, output any) Result {
	return Result{Benchmark: "test", Metrics: map[string]any{"input": input, "output": output}}
}

func TestUnifiedEvaluatorContract(t *testing.T) {
	var evaluator UnifiedEvaluator = testUnifiedEvaluator{}
	result := evaluator.Evaluate(map[string]any{"observed": true}, map[string]any{"expected": true})
	if result.Benchmark != "test" {
		t.Fatalf("unexpected unified result: %+v", result)
	}
}
