package evaluator

// The concrete evaluators make the common contract useful for orchestration
// while keeping each metric family independent. They are intentionally thin
// adapters over deterministic functions in metrics.go.

type RetrievalEvaluator struct{ Relevant []string }

func (RetrievalEvaluator) Name() string { return "retrieval" }
func (e RetrievalEvaluator) Evaluate(r IncidentResult) Score {
	m := EvaluateRanking([][]string{r.Observed.EvidenceIDs}, []map[string]float64{setRelevance(e.Relevant)})
	return Score{EvidencePrecision: m.PrecisionAt10, EvidenceRecall: m.RecallAt10}
}

type DiagnosisEvaluator struct{}

func (DiagnosisEvaluator) Name() string { return "diagnosis" }
func (DiagnosisEvaluator) Evaluate(r IncidentResult) Score {
	m := EvaluateDiagnosis([]IncidentResult{r})
	return Score{RootCauseCorrect: m.RootCauseAccuracy == 1, EvidencePrecision: m.EvidenceAttribution, EvidenceRecall: m.EvidenceAttribution, HypothesisRecall: m.HypothesisRecall, FalseHypothesisRate: m.FalseHypothesisRate, DiagnosisDurationMS: m.MeanDiagnosisMS}
}

type AgentEvaluator struct{}

func (AgentEvaluator) Name() string { return "agent" }
func (AgentEvaluator) Evaluate(r IncidentResult) Score {
	m := EvaluateAgent([]IncidentResult{r})
	return Score{DiagnosisDurationMS: m.AverageIterations}
}

type RecoveryEvaluator struct{}

func (RecoveryEvaluator) Name() string { return "recovery" }
func (RecoveryEvaluator) Evaluate(r IncidentResult) Score {
	m := EvaluateRecovery([]IncidentResult{r})
	return Score{RecoveryCorrect: m.RecoveryAccuracy == 1, VerificationSuccess: m.VerificationSuccess == 1}
}

type EvolutionEvaluator struct{ Expected []string }

func (EvolutionEvaluator) Name() string { return "evolution" }
func (e EvolutionEvaluator) Evaluate(r IncidentResult) Score {
	m := EvaluateEvolution(r.Observed.EvidenceIDs, e.Expected, 1)
	return Score{EvidencePrecision: m.PatternPrecision, EvidenceRecall: m.PatternRecall}
}

func setRelevance(ids []string) map[string]float64 {
	m := map[string]float64{}
	for _, id := range ids {
		m[id] = 1
	}
	return m
}
