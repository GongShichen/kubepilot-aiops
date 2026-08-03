package correlation

import (
	"github.com/kubepilot-aiops/kubepilot/benchmark/scorer"
	"testing"
)

func TestCorrelation100Groups(t *testing.T) {
	items := Generate(100, 2, 8, 20260803)
	score := scorer.Correlation(Expected(items), Correlate(items))
	if score.F1 < 0.99 {
		t.Fatalf("%#v", score)
	}
}
