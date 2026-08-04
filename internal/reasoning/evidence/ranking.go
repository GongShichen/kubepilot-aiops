package evidence

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/kubepilot-aiops/kubepilot/internal/domain"
	"gopkg.in/yaml.v3"
)

type Policy struct {
	Version  int                `yaml:"version"`
	Evidence map[string]float64 `yaml:"evidence"`
	Incident map[string]float64 `yaml:"incident"`
	Topology map[string]float64 `yaml:"topology"`
	Hash     string             `yaml:"-"`
}

// DefaultPolicy is used when a caller has not loaded an explicit policy file.
// It keeps deterministic and neural fusion explicit even in unit tests or
// embedded deployments that do not load knowledge/ranking_policy.yaml.
func DefaultPolicy() Policy {
	return Policy{
		Version: 1,
		Evidence: map[string]float64{
			"neural_similarity":             .30,
			"temporal_alignment":            .20,
			"service_resource_attribution":  .20,
			"trace_request_pod_attribution": .15,
			"causal_contribution":           .10,
			"source_quality_and_rarity":     .05,
		},
		Incident: map[string]float64{
			"neural_similarity":          .40,
			"topology_similarity":        .20,
			"normalized_rrf":             .15,
			"evidence_feature_overlap":   .10,
			"causal_path_coverage":       .05,
			"service_resource_proximity": .05,
			"revision_temporal_context":  .05,
		},
		Topology: map[string]float64{
			"directed_edge_jaccard":           .40,
			"shared_critical_dependency":      .30,
			"root_to_symptom_path_similarity": .20,
			"failing_node_role_similarity":    .10,
		},
	}
}

func LoadPolicy(path string) (Policy, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Policy{}, err
	}
	var policy Policy
	if err = yaml.Unmarshal(raw, &policy); err != nil {
		return Policy{}, err
	}
	for name, weights := range map[string]map[string]float64{"evidence": policy.Evidence, "incident": policy.Incident, "topology": policy.Topology} {
		if err = validateWeights(name, weights); err != nil {
			return Policy{}, err
		}
	}
	hash := sha256.Sum256(raw)
	policy.Hash = hex.EncodeToString(hash[:])
	return policy, nil
}

func validateWeights(name string, weights map[string]float64) error {
	if len(weights) == 0 {
		return fmt.Errorf("%s ranking weights are required", name)
	}
	total := 0.0
	for key, value := range weights {
		if strings.TrimSpace(key) == "" || value < 0 || value > 1 {
			return fmt.Errorf("invalid %s weight %q", name, key)
		}
		total += value
	}
	if total < .999999 || total > 1.000001 {
		return fmt.Errorf("%s ranking weights must sum to 1, got %.6f", name, total)
	}
	return nil
}

func Attribute(incident *domain.Incident, item domain.Evidence, causalContribution, anomalySpecificity float64) domain.EvidenceAttribution {
	if incident == nil {
		return domain.EvidenceAttribution{}
	}
	temporal := temporalAlignment(incident, item)
	service := serviceResourceMatch(incident, item)
	trace := tracePodMatch(incident, item)
	causal := clamp(causalContribution)
	anomaly := clamp(anomalySpecificity)
	score := .25*temporal + .30*service + .20*trace + .15*causal + .10*anomaly
	reasons := []string{fmt.Sprintf("temporal_alignment=%.3f", temporal), fmt.Sprintf("service_resource_match=%.3f", service), fmt.Sprintf("trace_request_pod_match=%.3f", trace), fmt.Sprintf("causal_contribution=%.3f", causal), fmt.Sprintf("anomaly_specificity=%.3f", anomaly)}
	return domain.EvidenceAttribution{TemporalAlignment: temporal, ServiceResourceMatch: service, TraceRequestPodMatch: trace, CausalContribution: causal, AnomalySpecificity: anomaly, AttributionScore: clamp(score), Reasons: reasons}
}

func Rank(policy Policy, incident *domain.Incident, items []domain.Evidence) []domain.Evidence {
	out := append([]domain.Evidence(nil), items...)
	for index := range out {
		attribution := Attribute(incident, out[index], causalValue(out[index]), anomalyValue(out[index]))
		out[index].Attribution = &attribution
		values := map[string]float64{
			"neural_similarity":             out[index].NeuralScore,
			"temporal_alignment":            attribution.TemporalAlignment,
			"service_resource_attribution":  attribution.ServiceResourceMatch,
			"trace_request_pod_attribution": attribution.TraceRequestPodMatch,
			"causal_contribution":           attribution.CausalContribution,
			"source_quality_and_rarity":     (sourceQuality(out[index]) + attribution.AnomalySpecificity) / 2,
		}
		// The policy controls the deterministic feature composition. Neural
		// reranking is fused separately so an unavailable API can never be
		// mistaken for a zero-valued model score.
		deterministic := weighted(values, withoutAndNormalize(policy.Evidence, "neural_similarity"))
		final := deterministic
		if out[index].NeuralRanked {
			final = clamp(.70*deterministic + .30*clamp(out[index].NeuralScore))
		}
		factors := map[string]float64{}
		for key, value := range values {
			factors[key] = value
		}
		factors["deterministic_score"] = deterministic
		factors["final_score"] = final
		out[index].RankBreakdown = &domain.EvidenceRankBreakdown{
			EvidenceID:          out[index].ID,
			DeterministicScore:  deterministic,
			NeuralScore:         out[index].NeuralScore,
			DeterministicWeight: .70,
			NeuralWeight:        .30,
			FinalScore:          final,
			NeuralUsed:          out[index].NeuralRanked,
			Factors:             factors,
		}
		out[index].RelevanceScore = final
		out[index].RankingReasons = append(out[index].RankingReasons, attribution.Reasons...)
		out[index].RankingReasons = append(out[index].RankingReasons, fmt.Sprintf("evidence_deterministic=%.3f", deterministic), fmt.Sprintf("evidence_neural_used=%t", out[index].NeuralRanked), fmt.Sprintf("final_relevance=%.3f", out[index].RelevanceScore))
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].RelevanceScore == out[j].RelevanceScore {
			return out[i].ID < out[j].ID
		}
		return out[i].RelevanceScore > out[j].RelevanceScore
	})
	return out
}

func RankCandidates(policy Policy, items []domain.RetrievalCandidate) []domain.RetrievalCandidate {
	out := append([]domain.RetrievalCandidate(nil), items...)
	for index := range out {
		values := map[string]float64{
			"neural_similarity":          out[index].Rank.NeuralSimilarity,
			"topology_similarity":        out[index].Rank.TopologySimilarity,
			"normalized_rrf":             out[index].Rank.NormalizedRRF,
			"evidence_feature_overlap":   out[index].Rank.EvidenceFeatureOverlap,
			"causal_path_coverage":       out[index].Rank.CausalPathCoverage,
			"service_resource_proximity": out[index].Rank.ServiceResourceProximity,
			"revision_temporal_context":  out[index].Rank.RevisionTemporalContext,
		}
		deterministic := weighted(values, withoutAndNormalize(policy.Incident, "neural_similarity"))
		if out[index].Rank.DeterministicScore != 0 {
			deterministic = out[index].Rank.DeterministicScore
		}
		final := deterministic
		if out[index].Rank.NeuralRanked {
			final = clamp(.45*deterministic + .55*clamp(out[index].Rank.NeuralSimilarity))
		}
		out[index].Rank.DeterministicScore = deterministic
		out[index].Rank.FinalScore = final
		factors := map[string]float64{}
		for key, value := range values {
			factors[key] = value
		}
		factors["deterministic_score"] = deterministic
		factors["final_score"] = final
		out[index].Rank.IncidentRank = &domain.IncidentRankBreakdown{
			IncidentID:          out[index].IncidentID,
			DeterministicScore:  deterministic,
			TopologyScore:       out[index].Rank.TopologySimilarity,
			NeuralScore:         out[index].Rank.NeuralSimilarity,
			DeterministicWeight: .45,
			NeuralWeight:        .55,
			FinalScore:          final,
			NeuralUsed:          out[index].Rank.NeuralRanked,
			Factors:             factors,
		}
		out[index].RankingReasons = append(out[index].RankingReasons, fmt.Sprintf("incident_deterministic=%.3f", deterministic), fmt.Sprintf("incident_neural_used=%t", out[index].Rank.NeuralRanked), fmt.Sprintf("policy_final=%.3f", out[index].Rank.FinalScore))
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Rank.FinalScore == out[j].Rank.FinalScore {
			return out[i].IncidentID < out[j].IncidentID
		}
		return out[i].Rank.FinalScore > out[j].Rank.FinalScore
	})
	return out
}

func temporalAlignment(incident *domain.Incident, item domain.Evidence) float64 {
	t := item.Timestamp
	if t.IsZero() {
		t = item.ObservedAt
	}
	if t.IsZero() {
		return .25
	}
	target := incident.CreatedAt
	if target.IsZero() {
		target = time.Now().UTC()
	}
	delta := target.Sub(t)
	if delta < 0 {
		delta = -delta
	}
	window := 5 * time.Minute
	if !item.WindowStart.IsZero() && !item.WindowEnd.IsZero() && item.WindowEnd.After(item.WindowStart) {
		window = item.WindowEnd.Sub(item.WindowStart)
	}
	if delta >= window {
		return 0
	}
	return clamp(1 - float64(delta)/float64(window))
}

func serviceResourceMatch(incident *domain.Incident, item domain.Evidence) float64 {
	score := 0.0
	if item.Namespace == incident.Namespace {
		score += .25
	}
	if item.Service == incident.Service {
		score += .45
	}
	if item.Resource != "" && item.Resource == incident.Resource {
		score += .30
	}
	return clamp(score)
}

func tracePodMatch(incident *domain.Incident, item domain.Evidence) float64 {
	score := 0.0
	if item.TraceID != "" && (incident.TraceID == "" || item.TraceID == incident.TraceID) {
		score += .60
	}
	text := strings.ToLower(item.Summary + " " + fmt.Sprint(item.Content))
	if incident.Resource != "" && strings.Contains(text, strings.ToLower(incident.Resource)) {
		score += .40
	}
	return clamp(score)
}

func causalValue(item domain.Evidence) float64 {
	if len(item.CausalNodeIDs) == 0 {
		return 0
	}
	return clamp(float64(len(item.CausalNodeIDs)) / 3)
}
func anomalyValue(item domain.Evidence) float64 {
	if item.Confidence > 0 {
		return clamp(item.Confidence)
	}
	return .5
}
func sourceQuality(item domain.Evidence) float64 {
	if item.Source == "kubernetes" {
		return 1
	}
	if item.Source == "prometheus" {
		return .95
	}
	if item.Source == "jaeger" {
		return .9
	}
	if item.Source == "loki" {
		return .85
	}
	return .6
}
func weighted(values, weights map[string]float64) float64 {
	total := 0.0
	for key, weight := range weights {
		total += weight * clamp(values[key])
	}
	return clamp(total)
}
func withoutAndNormalize(weights map[string]float64, excluded string) map[string]float64 {
	total := 0.0
	out := map[string]float64{}
	for key, value := range weights {
		if key != excluded {
			out[key] = value
			total += value
		}
	}
	if total > 0 {
		for key := range out {
			out[key] /= total
		}
	}
	return out
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
