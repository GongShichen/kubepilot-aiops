// Package retrieval is a component-level evaluator. It accepts ranked IDs
// from semantic, lexical, topology, hybrid, or reranked implementations and
// does not depend on any concrete backend.
package retrieval

import (
	"fmt"
	"sort"

	"github.com/kubepilot-aiops/kubepilot/benchmark/evaluator"
)

type Query struct {
	ID       string
	Text     string
	Relevant map[string]float64
}

type Result struct {
	QueryID   string
	Strategy  string
	RankedIDs []string
}

type Metrics struct {
	Strategy string `json:"strategy"`
	evaluator.RankingMetrics
}

func Evaluate(strategy string, queries []Query, results []Result) Metrics {
	byID := make(map[string]Result, len(results))
	for _, result := range results {
		byID[result.QueryID] = result
	}
	rankings := make([][]string, 0, len(queries))
	relevant := make([]map[string]float64, 0, len(queries))
	for _, query := range queries {
		result, ok := byID[query.ID]
		if !ok {
			rankings = append(rankings, nil)
		} else {
			rankings = append(rankings, result.RankedIDs)
		}
		relevant = append(relevant, query.Relevant)
	}
	return Metrics{Strategy: strategy, RankingMetrics: evaluator.EvaluateRanking(rankings, relevant)}
}

type Suite struct {
	Queries []Query
	Results map[string][]Result
}
type Report struct {
	Dataset    string    `json:"dataset"`
	Strategies []Metrics `json:"strategies"`
}

func (s Suite) Evaluate(dataset string) Report {
	out := Report{Dataset: dataset}
	strategies := make([]string, 0, len(s.Results))
	for strategy := range s.Results {
		strategies = append(strategies, strategy)
	}
	sort.Strings(strategies)
	for _, strategy := range strategies {
		results := s.Results[strategy]
		out.Strategies = append(out.Strategies, Evaluate(strategy, s.Queries, results))
	}
	return out
}

func ValidateStrategy(strategy string) error {
	switch strategy {
	case "semantic", "lexical", "topology", "hybrid", "reranker":
		return nil
	default:
		return fmt.Errorf("unsupported retrieval strategy %q", strategy)
	}
}
