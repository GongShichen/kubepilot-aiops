package discovery

import (
	"sort"
	"strings"
	"time"
)

type PatternMiner struct {
	MaxPathLength int
}

func NewPatternMiner() PatternMiner { return PatternMiner{MaxPathLength: 6} }

// Mine uses the default bounded path miner for callers that do not need to
// customize the maximum causal path length.
func Mine(graphs []IncidentCausalGraph) []CausalPatternCandidate {
	return NewPatternMiner().Mine(graphs)
}

func (m PatternMiner) Mine(graphs []IncidentCausalGraph) []CausalPatternCandidate {
	if m.MaxPathLength < 2 {
		m.MaxPathLength = 6
	}
	type aggregate struct {
		candidate CausalPatternCandidate
		evidence  float64
		observed  int
	}
	aggregates := map[string]*aggregate{}
	eligible := 0
	for _, graph := range graphs {
		if graph.IncidentID == "" {
			continue
		}
		eligible++
		paths := causalPaths(graph, m.MaxPathLength)
		present := map[string]bool{}
		for _, path := range paths {
			key := pathKey(path)
			present[key] = true
			item := aggregates[key]
			if item == nil {
				item = &aggregate{candidate: CausalPatternCandidate{PatternID: PatternID(path), CausalPath: path, Status: StatusDiscovered, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}}
				aggregates[key] = item
			}
			if !contains(item.candidate.SupportingIncidents, graph.IncidentID) {
				item.candidate.SupportingIncidents = append(item.candidate.SupportingIncidents, graph.IncidentID)
				item.candidate.Frequency++
			}
			item.evidence += pathEvidenceConfidence(graph, path)
			item.observed++
		}
		// A graph that contains the candidate's first node but not the complete
		// path is an explicit counter-observation, not unrelated noise.
		for key, item := range aggregates {
			if present[key] {
				continue
			}
			if graphHasNode(graph, item.candidate.CausalPath[0]) {
				if !contains(item.candidate.Contradictions, graph.IncidentID) {
					item.candidate.Contradictions = append(item.candidate.Contradictions, graph.IncidentID)
				}
			}
		}
	}
	// A path that has exactly the same supporting Incident set as a longer
	// path is a redundant prefix. Keeping only the maximal supported path
	// avoids flooding the knowledge store with fragments while preserving
	// shared prefixes when their support differs.
	for key, item := range aggregates {
		for otherKey, other := range aggregates {
			if key == otherKey || len(other.candidate.CausalPath) <= len(item.candidate.CausalPath) || !sameSupport(item.candidate.SupportingIncidents, other.candidate.SupportingIncidents) || !isSubpath(item.candidate.CausalPath, other.candidate.CausalPath) {
				continue
			}
			delete(aggregates, key)
			break
		}
	}
	maxFrequency := 1
	for _, item := range aggregates {
		if item.candidate.Frequency > maxFrequency {
			maxFrequency = item.candidate.Frequency
		}
	}
	result := make([]CausalPatternCandidate, 0, len(aggregates))
	for _, item := range aggregates {
		if item.candidate.Frequency == 0 {
			continue
		}
		item.candidate.Coverage = clamp(float64(item.candidate.Frequency) / float64(maxInt(eligible, 1)))
		if item.observed > 0 {
			item.candidate.EvidenceConfidence = clamp(item.evidence / float64(item.observed))
		}
		contradictionRatio := 0.0
		if item.candidate.Frequency > 0 {
			contradictionRatio = clamp(float64(len(item.candidate.Contradictions)) / float64(item.candidate.Frequency))
		}
		item.candidate.CausalConsistency = clamp(1 - contradictionRatio)
		item.candidate.ContradictionPenalty = .20 * contradictionRatio
		item.candidate.Confidence, _ = ScoreCandidate(item.candidate, eligible, maxFrequency)
		item.candidate.SupportingIncidents = unique(item.candidate.SupportingIncidents)
		item.candidate.Contradictions = unique(item.candidate.Contradictions)
		item.candidate.UpdatedAt = time.Now().UTC()
		result = append(result, item.candidate)
	}
	sort.SliceStable(result, func(i, j int) bool {
		if result[i].Confidence != result[j].Confidence {
			return result[i].Confidence > result[j].Confidence
		}
		return result[i].PatternID < result[j].PatternID
	})
	return result
}

func causalPaths(graph IncidentCausalGraph, maxLength int) [][]string {
	names := map[string]string{}
	for _, node := range graph.Nodes {
		if node.Name != "" {
			names[node.ID] = normalizeName(node.Name)
		}
	}
	adj := map[string][]string{}
	for _, edge := range graph.Edges {
		if (edge.Relation != "causes" && edge.Relation != "manifests_as") || names[edge.From] == "" || names[edge.To] == "" {
			continue
		}
		adj[edge.From] = append(adj[edge.From], edge.To)
	}
	for key := range adj {
		sort.Strings(adj[key])
	}
	starts := make([]string, 0, len(adj))
	for start := range adj {
		starts = append(starts, start)
	}
	sort.Strings(starts)
	paths := [][]string{}
	for _, start := range starts {
		dfsPaths(start, []string{start}, adj, names, maxLength, &paths)
	}
	return paths
}

func dfsPaths(current string, nodes []string, adj map[string][]string, names map[string]string, maxLength int, out *[][]string) {
	if len(nodes) >= 2 {
		path := make([]string, 0, len(nodes))
		for _, node := range nodes {
			path = append(path, names[node])
		}
		*out = append(*out, path)
	}
	if len(nodes) >= maxLength {
		return
	}
	for _, next := range adj[current] {
		seen := false
		for _, existing := range nodes {
			if existing == next {
				seen = true
				break
			}
		}
		if !seen {
			dfsPaths(next, append(nodes, next), adj, names, maxLength, out)
		}
	}
}

func pathKey(path []string) string { return strings.Join(path, "->") }

func pathEvidenceConfidence(graph IncidentCausalGraph, path []string) float64 {
	confidence := []float64{}
	names := map[string]string{}
	for _, node := range graph.Nodes {
		names[node.ID] = normalizeName(node.Name)
	}
	for _, edge := range graph.Edges {
		if edge.Relation != "causes" && edge.Relation != "manifests_as" {
			continue
		}
		for index := 0; index+1 < len(path); index++ {
			if names[edge.From] == path[index] && names[edge.To] == path[index+1] {
				confidence = append(confidence, clamp(edge.Confidence))
				break
			}
		}
	}
	for _, node := range graph.Nodes {
		for _, name := range path {
			if normalizeName(node.Name) == name && node.Confidence > 0 {
				confidence = append(confidence, clamp(node.Confidence))
			}
		}
	}
	if len(confidence) == 0 {
		return .5
	}
	sum := 0.0
	for _, value := range confidence {
		sum += value
	}
	return clamp(sum / float64(len(confidence)))
}

func graphHasNode(graph IncidentCausalGraph, name string) bool {
	name = normalizeName(name)
	for _, node := range graph.Nodes {
		if normalizeName(node.Name) == name {
			return true
		}
	}
	return false
}

func isSubpath(needle, path []string) bool {
	if len(needle) >= len(path) {
		return false
	}
	for start := 0; start+len(needle) <= len(path); start++ {
		matched := true
		for offset := range needle {
			if needle[offset] != path[start+offset] {
				matched = false
				break
			}
		}
		if matched {
			return true
		}
	}
	return false
}

func sameSupport(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for _, item := range left {
		if !contains(right, item) {
			return false
		}
	}
	return true
}

func maxInt(left, right int) int {
	if left > right {
		return left
	}
	return right
}
