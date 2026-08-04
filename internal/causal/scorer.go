package causal

func ScoreHypothesis(input ScoreInput) HypothesisScore {
	input.ModelPrior = clamp(input.ModelPrior)
	input.EvidenceSupport = clamp(input.EvidenceSupport)
	input.CausalCoverage = clamp(input.CausalCoverage)
	input.TopologyMatch = clamp(input.TopologyMatch)
	input.HistoricalSimilarity = clamp(input.HistoricalSimilarity)
	input.Contradiction = clamp(input.Contradiction)
	positive := .30*input.EvidenceSupport + .25*input.CausalCoverage + .20*input.TopologyMatch + .15*input.HistoricalSimilarity + .10*input.ModelPrior
	score := clamp(positive - .30*input.Contradiction)
	return HypothesisScore{Score: score, EvidenceSupport: input.EvidenceSupport, CausalCoverage: input.CausalCoverage, TopologyMatch: input.TopologyMatch, HistoricalSimilarity: input.HistoricalSimilarity, ModelPrior: input.ModelPrior, Contradiction: input.Contradiction, Breakdown: map[string]float64{"evidence_support": .30 * input.EvidenceSupport, "causal_coverage": .25 * input.CausalCoverage, "topology_match": .20 * input.TopologyMatch, "historical_similarity": .15 * input.HistoricalSimilarity, "model_prior": .10 * input.ModelPrior, "contradiction_penalty": -.30 * input.Contradiction}}
}

func clamp(value float64) float64 {
	if value < 0 {
		return 0
	}
	if value > 1 {
		return 1
	}
	return value
}
