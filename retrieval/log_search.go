package retrieval

import (
	"sort"
	"strings"
	"unicode"

	"github.com/kubepilot-aiops/kubepilot/tools"
)

// RankLogTemplates is the canonical lexical template ranking capability for
// Loki entries. It is independent of evaluation data and can be reused by API,
// Tool, and offline callers.
func RankLogTemplates(query string, entries []tools.LokiEntry) []string {
	queryTokens := logSearchTokens(query)
	scores := map[string]float64{}
	for _, entry := range entries {
		templateID := entry.Labels["template_id"]
		if templateID == "" {
			continue
		}
		overlap := 0.0
		entryTokens := logSearchTokens(entry.Line)
		for token := range queryTokens {
			if entryTokens[token] {
				overlap++
			}
		}
		score := overlap / float64(maximum(1, len(queryTokens)))
		if score > scores[templateID] {
			scores[templateID] = score
		}
	}
	type rankedTemplate struct {
		id    string
		score float64
	}
	ranked := make([]rankedTemplate, 0, len(scores))
	for id, score := range scores {
		ranked = append(ranked, rankedTemplate{id: id, score: score})
	}
	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].score == ranked[j].score {
			return ranked[i].id < ranked[j].id
		}
		return ranked[i].score > ranked[j].score
	})
	ids := make([]string, len(ranked))
	for index := range ranked {
		ids[index] = ranked[index].id
	}
	return ids
}

func logSearchTokens(text string) map[string]bool {
	tokens := map[string]bool{}
	for _, token := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		if len([]rune(token)) >= 3 {
			tokens[token] = true
		}
	}
	return tokens
}

func maximum(left, right int) int {
	if left > right {
		return left
	}
	return right
}
