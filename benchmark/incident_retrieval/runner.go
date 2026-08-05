package incident_retrieval

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
	"time"

	rerankerclient "github.com/kubepilot-aiops/kubepilot/internal/retrieval/reranker"
)

type RunnerConfig struct {
	DatasetPath string
	Count       int
	OutputDir   string
	Reranker    rerankerclient.Service
	Progress    func(current, total int)
}

const (
	incidentCandidateTopK = 100
	incidentReasoningTopK = 20
)

// Run executes the incident-only retrieval suite. Semantic and lexical
// signals generate the candidate pool; topology and causal signals only
// rerank those candidates. The optional external reranker is used only by the
// full strategy and its scores are part of the final ranking.
func Run(ctx context.Context, cfg RunnerConfig) (Report, error) {
	dataset, err := LoadExpanded(cfg.DatasetPath, cfg.Count)
	if err != nil {
		return Report{}, err
	}
	observations := make([]Observation, 0, len(dataset.Incidents)*len(AblationStrategies))
	for index, query := range dataset.Incidents {
		if err := ctx.Err(); err != nil {
			return Report{}, err
		}
		for _, strategy := range AblationStrategies {
			started := time.Now()
			ranked, rankErr := rankIncident(ctx, query, dataset.Incidents, strategy, cfg.Reranker)
			if rankErr != nil {
				return Report{}, rankErr
			}
			observations = append(observations, Observation{QueryID: query.IncidentID, Strategy: strategy, RankedIDs: ranked, Latency: time.Since(started)})
		}
		if cfg.Progress != nil {
			cfg.Progress(index+1, len(dataset.Incidents))
		}
	}
	report := Evaluate(dataset, observations)
	if cfg.OutputDir != "" {
		if err := os.MkdirAll(cfg.OutputDir, 0o750); err != nil {
			return Report{}, err
		}
		b, _ := json.MarshalIndent(report, "", "  ")
		if err := os.WriteFile(cfg.OutputDir+"/incident_retrieval_report.json", b, 0o640); err != nil {
			return Report{}, err
		}
	}
	return report, nil
}

func rankIncident(ctx context.Context, query Incident, candidates []Incident, strategy string, neural rerankerclient.Service) ([]string, error) {
	type scored struct {
		id            string
		deterministic float64
		neural        float64
	}
	items := make([]scored, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.IncidentID == query.IncidentID {
			continue
		}
		semantic := overlap(tokenSet(queryText(query)), tokenSet(candidateText(candidate)))
		lexical := overlap(queryTokens(query), candidateTokens(candidate))
		if strategy == StrategySemantic {
			lexical = 0
		}
		base := .6*semantic + .4*lexical
		if strategy == StrategySemantic {
			base = semantic
		}
		if base == 0 {
			base = .001
		}
		topology, causal := 0.0, 0.0
		if strategy == StrategySemanticLexicalTopology || strategy == StrategySemanticLexicalCausal || strategy == StrategyFull {
			topology = topologyScore(query, candidate)
		}
		if strategy == StrategySemanticLexicalCausal || strategy == StrategyFull {
			causal = causalScore(query, candidate)
		}
		reasoning := .35*semantic + .20*lexical + .20*topology + .15*causal + .10*metadataScore(query, candidate)
		if strategy == StrategySemantic {
			reasoning = semantic
		}
		if strategy == StrategySemanticLexical {
			reasoning = .6*semantic + .4*lexical
		}
		if strategy == StrategySemanticLexicalTopology {
			reasoning = .35*semantic + .20*lexical + .20*topology + .10*metadataScore(query, candidate)
		}
		if strategy == StrategySemanticLexicalCausal {
			reasoning = .35*semantic + .20*lexical + .15*causal + .10*metadataScore(query, candidate)
		}
		items = append(items, scored{id: candidate.IncidentID, deterministic: reasoning + .05*base})
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].deterministic == items[j].deterministic {
			return items[i].id < items[j].id
		}
		return items[i].deterministic > items[j].deterministic
	})
	if len(items) > incidentCandidateTopK {
		items = items[:incidentCandidateTopK]
	}
	if strategy == StrategyFull && neural != nil && neural.Enabled() && len(items) > 0 {
		// Neural reranking is a final-stage operation.  Keep the request bounded
		// to the reasoning shortlist instead of sending the complete historical
		// corpus to the external API.  This preserves the high-recall generator
		// while respecting the provider payload contract.
		shortlistSize := len(items)
		if shortlistSize > incidentReasoningTopK {
			shortlistSize = incidentReasoningTopK
		}
		shortlist := append([]scored(nil), items[:shortlistSize]...)
		documents := make([]string, len(shortlist))
		for i, item := range shortlist {
			for _, candidate := range candidates {
				if candidate.IncidentID == item.id {
					documents[i] = candidateDocument(candidate)
					break
				}
			}
		}
		results, err := neural.Rerank(ctx, candidateDocument(query), documents, len(documents))
		if err != nil {
			return nil, fmt.Errorf("incident reranker: %w", err)
		}
		for _, result := range results {
			if result.Index >= 0 && result.Index < len(shortlist) {
				shortlist[result.Index].neural = result.Score
			}
		}
		max := 1.0
		for _, item := range shortlist {
			if item.neural > max {
				max = item.neural
			}
		}
		for i := range shortlist {
			shortlist[i].deterministic = .6*shortlist[i].deterministic + .4*(shortlist[i].neural/max)
		}
		sort.SliceStable(shortlist, func(i, j int) bool {
			if shortlist[i].deterministic == shortlist[j].deterministic {
				return shortlist[i].id < shortlist[j].id
			}
			return shortlist[i].deterministic > shortlist[j].deterministic
		})
		items = append(shortlist, items[shortlistSize:]...)
		// The shortlist is already sorted by the final neural/deterministic
		// fusion. The remainder retains deterministic generator order.
	}
	out := make([]string, len(items))
	for i := range items {
		out[i] = items[i].id
	}
	return out, nil
}

func queryText(i Incident) string {
	return strings.Join(append(append(append(append(append([]string{i.Service, i.Namespace}, i.Symptoms...), i.Metrics...), i.Logs...), i.Traces...), i.CausalFeatures...), " ")
}
func candidateText(i Incident) string {
	return queryText(i) + " " + strings.Join(i.KubernetesEvents, " ")
}
func queryTokens(i Incident) map[string]bool     { return tokenSet(queryText(i)) }
func candidateTokens(i Incident) map[string]bool { return tokenSet(candidateText(i)) }
func tokenSet(s string) map[string]bool {
	out := map[string]bool{}
	for _, t := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return r < 'a' || r > 'z' }) {
		if len(t) > 2 {
			out[t] = true
		}
	}
	return out
}
func overlap(a, b map[string]bool) float64 {
	if len(a) == 0 {
		return 0
	}
	n := 0
	for t := range a {
		if b[t] {
			n++
		}
	}
	return float64(n) / float64(len(a))
}
func topologyScore(a, b Incident) float64 {
	at := map[string]bool{}
	bt := map[string]bool{}
	for _, n := range a.TopologyGraph.Nodes {
		at[n.Type] = true
	}
	for _, n := range b.TopologyGraph.Nodes {
		bt[n.Type] = true
	}
	nodes := overlap(at, bt)
	edges := 0.0
	for _, e := range a.TopologyGraph.Edges {
		for _, f := range b.TopologyGraph.Edges {
			if e.Type == f.Type {
				edges += 1
				break
			}
		}
	}
	if len(a.TopologyGraph.Edges) > 0 {
		edges /= float64(len(a.TopologyGraph.Edges))
	}
	return .6*nodes + .4*edges
}
func causalScore(a, b Incident) float64 {
	return overlap(tokenSet(strings.Join(a.CausalFeatures, " ")), tokenSet(strings.Join(b.CausalFeatures, " ")))
}
func metadataScore(a, b Incident) float64 {
	if a.Namespace == b.Namespace {
		return .6
	}
	return 0
}
func candidateDocument(i Incident) string {
	return fmt.Sprintf("service=%s namespace=%s symptoms=%s metrics=%s logs=%s traces=%s topology=%s causal=%s", i.Service, i.Namespace, strings.Join(i.Symptoms, ","), strings.Join(i.Metrics, ","), strings.Join(i.Logs, ","), strings.Join(i.Traces, ","), topologyText(i), strings.Join(i.CausalFeatures, ","))
}
func topologyText(i Incident) string {
	parts := make([]string, 0, len(i.TopologyGraph.Edges))
	for _, e := range i.TopologyGraph.Edges {
		parts = append(parts, e.Source+"->"+e.Target+":"+e.Type)
	}
	return strings.Join(parts, " ")
}

var _ = math.MaxFloat64
