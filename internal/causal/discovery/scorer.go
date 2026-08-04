package discovery

// ScoreCandidate applies the deterministic discovery score. Frequency is
// normalized against the strongest path in the same mining batch, making the
// score comparable within a discovery run while preserving the published
// weights.
func ScoreCandidate(candidate CausalPatternCandidate, totalGraphs, maxFrequency int) (float64, float64) {
	if totalGraphs <= 0 {
		totalGraphs = 1
	}
	if maxFrequency <= 0 {
		maxFrequency = 1
	}
	frequency := clamp(float64(candidate.Frequency) / float64(maxFrequency))
	coverage := clamp(candidate.Coverage)
	evidence := clamp(candidate.EvidenceConfidence)
	contradictions := 0.0
	if candidate.Frequency > 0 {
		contradictions = clamp(float64(len(candidate.Contradictions)) / float64(candidate.Frequency))
	}
	consistency := clamp(1 - contradictions)
	penalty := .20 * contradictions
	score := .35*frequency + .25*coverage + .20*evidence + .20*consistency - penalty
	return clamp(score), penalty
}
