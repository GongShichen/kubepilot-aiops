package knowledge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/kubepilot-aiops/kubepilot/internal/topology"
)

// TopologyNode is an incident-independent node in a learned service pattern.
// Concrete pod names, IPs and replica identifiers are deliberately absent.
type TopologyNode struct {
	Name     string            `json:"name"`
	Type     string            `json:"type"`
	Role     string            `json:"role,omitempty"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

type TopologyEdge struct {
	Source string  `json:"source"`
	Target string  `json:"target"`
	Type   string  `json:"type"`
	Weight float64 `json:"weight,omitempty"`
}

type ServiceTopologyPattern struct {
	PatternID       string         `json:"pattern_id"`
	Nodes           []TopologyNode `json:"nodes"`
	Edges           []TopologyEdge `json:"edges"`
	Frequency       int            `json:"frequency"`
	Confidence      float64        `json:"confidence"`
	SourceIncidents []string       `json:"source_incidents,omitempty"`
	LastObserved    time.Time      `json:"last_observed"`
}

var ErrNotFound = errors.New("topology knowledge pattern not found")

type Reader interface {
	List(context.Context, int) ([]ServiceTopologyPattern, error)
	Search(context.Context, topology.IncidentGraph, int) ([]ServiceTopologyPattern, error)
}

type PatternStore interface {
	Reader
	Merge(context.Context, ServiceTopologyPattern) (ServiceTopologyPattern, error)
}

// Normalize removes runtime instance identity and retains only reusable
// dependency roles. A service is intentionally generalized to
// business-service so payment/order/checkout incidents can share a pattern.
func Normalize(graph topology.IncidentGraph) ServiceTopologyPattern {
	graph = graph.Normalize()
	if len(graph.Nodes) == 0 {
		return ServiceTopologyPattern{}
	}
	nameByID := map[string]string{}
	nodes := make([]TopologyNode, 0, len(graph.Nodes))
	for _, node := range graph.Nodes {
		typ := normalizeType(node.Type, node.ID)
		name := canonicalName(typ, node.ID)
		if typ == "pod" || typ == "deployment" {
			// Workload instances are represented by their owning service when
			// possible, and otherwise omitted from a reusable dependency pattern.
			continue
		}
		nameByID[node.ID] = name
		nodes = append(nodes, TopologyNode{Name: name, Type: typ, Role: node.Metadata["role"]})
	}
	edges := make([]TopologyEdge, 0, len(graph.Edges))
	for _, edge := range graph.Edges {
		source, okSource := nameByID[edge.Source]
		target, okTarget := nameByID[edge.Target]
		if !okSource || !okTarget || source == target {
			continue
		}
		edges = append(edges, TopologyEdge{Source: source, Target: target, Type: strings.ToLower(strings.TrimSpace(edge.Relation)), Weight: edge.Weight})
	}
	pattern := ServiceTopologyPattern{Nodes: uniqueNodes(nodes), Edges: uniqueEdges(edges), LastObserved: time.Now().UTC()}
	pattern.PatternID = ID(pattern)
	return pattern
}

func ID(pattern ServiceTopologyPattern) string {
	canonical := pattern
	canonical.PatternID = ""
	canonical.Frequency = 0
	canonical.Confidence = 0
	canonical.SourceIncidents = nil
	canonical.LastObserved = time.Time{}
	raw, _ := json.Marshal(canonical)
	hash := sha256.Sum256(raw)
	return "topology-" + hex.EncodeToString(hash[:8])
}

func Merge(left, right ServiceTopologyPattern) ServiceTopologyPattern {
	if left.PatternID == "" {
		right.Frequency = max(1, right.Frequency)
		if right.LastObserved.IsZero() {
			right.LastObserved = time.Now().UTC()
		}
		right.Confidence = confidence(right.Frequency)
		return right
	}
	left.PatternID = ID(left)
	newIncidentCount := 0
	for _, id := range right.SourceIncidents {
		found := false
		for _, oldID := range left.SourceIncidents {
			if oldID == id {
				found = true
				break
			}
		}
		if !found {
			newIncidentCount++
		}
	}
	if len(right.SourceIncidents) == 0 {
		newIncidentCount = max(1, right.Frequency)
	}
	left.Frequency += newIncidentCount
	left.SourceIncidents = uniqueStrings(append(left.SourceIncidents, right.SourceIncidents...))
	if right.LastObserved.After(left.LastObserved) {
		left.LastObserved = right.LastObserved
	}
	if left.Frequency < len(left.SourceIncidents) {
		left.Frequency = len(left.SourceIncidents)
	}
	left.Confidence = confidence(left.Frequency)
	return left
}

func confidence(frequency int) float64 {
	if frequency <= 0 {
		return 0
	}
	// Repeated independent observations increase confidence, while retaining
	// uncertainty for patterns seen only once.
	value := 1 - exp(-float64(frequency)/5)
	if value > 1 {
		return 1
	}
	return value
}

func normalizeType(value, id string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		value = "service"
	}
	if value == "datastore" || value == "critical_dependency" {
		value = "database"
	}
	if value == "external" || value == "external_service" {
		value = "external-api"
	}
	if value == "service" && strings.Contains(strings.ToLower(id), "redis") {
		return "cache"
	}
	return value
}

func canonicalName(typ, id string) string {
	lower := strings.ToLower(strings.TrimSpace(id))
	switch typ {
	case "service":
		return "business-service"
	case "database":
		if strings.Contains(lower, "mysql") {
			return "mysql"
		}
		if strings.Contains(lower, "postgres") {
			return "postgres"
		}
		return "database"
	case "cache":
		if strings.Contains(lower, "redis") {
			return "redis"
		}
		return "cache"
	case "queue":
		return "queue"
	case "external-api":
		return "external-api"
	default:
		return typ
	}
}

func uniqueNodes(values []TopologyNode) []TopologyNode {
	seen := map[string]bool{}
	out := make([]TopologyNode, 0, len(values))
	for _, value := range values {
		key := value.Type + ":" + value.Name
		if !seen[key] {
			seen[key] = true
			out = append(out, value)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Type+out[i].Name < out[j].Type+out[j].Name })
	return out
}

func uniqueEdges(values []TopologyEdge) []TopologyEdge {
	seen := map[string]bool{}
	out := make([]TopologyEdge, 0, len(values))
	for _, value := range values {
		key := value.Source + ">" + value.Target + ":" + value.Type
		if !seen[key] {
			seen[key] = true
			out = append(out, value)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Source+out[i].Target+out[i].Type < out[j].Source+out[j].Target+out[j].Type
	})
	return out
}

func uniqueStrings(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value != "" && !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	sort.Strings(out)
	return out
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
func exp(v float64) float64 {
	// Small local approximation is sufficient for the bounded confidence
	// curve and keeps this package dependency-free.
	term, sum := 1.0, 1.0
	for i := 1; i < 18; i++ {
		term *= v / float64(i)
		sum += term
	}
	return sum
}

type MemoryStore struct {
	mu       sync.RWMutex
	patterns map[string]ServiceTopologyPattern
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{patterns: map[string]ServiceTopologyPattern{}}
}
func (s *MemoryStore) List(_ context.Context, limit int) ([]ServiceTopologyPattern, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]ServiceTopologyPattern, 0, len(s.patterns))
	for _, value := range s.patterns {
		out = append(out, value)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Confidence == out[j].Confidence {
			return out[i].PatternID < out[j].PatternID
		}
		return out[i].Confidence > out[j].Confidence
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
func (s *MemoryStore) Search(_ context.Context, query topology.IncidentGraph, limit int) ([]ServiceTopologyPattern, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	type scored struct {
		pattern ServiceTopologyPattern
		score   float64
	}
	items := make([]scored, 0, len(s.patterns))
	queryPattern := Normalize(query)
	for _, pattern := range s.patterns {
		items = append(items, scored{pattern: pattern, score: topologyPatternSimilarity(queryPattern, pattern)})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].score == items[j].score {
			return items[i].pattern.PatternID < items[j].pattern.PatternID
		}
		return items[i].score > items[j].score
	})
	out := make([]ServiceTopologyPattern, 0, len(items))
	for _, item := range items {
		out = append(out, item.pattern)
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
func (s *MemoryStore) Merge(_ context.Context, pattern ServiceTopologyPattern) (ServiceTopologyPattern, error) {
	if s == nil || pattern.PatternID == "" {
		return ServiceTopologyPattern{}, ErrNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	pattern.Frequency = max(1, pattern.Frequency)
	if pattern.LastObserved.IsZero() {
		pattern.LastObserved = time.Now().UTC()
	}
	old, ok := s.patterns[pattern.PatternID]
	if ok {
		pattern = Merge(old, pattern)
	} else {
		pattern.Confidence = confidence(pattern.Frequency)
	}
	s.patterns[pattern.PatternID] = pattern
	return pattern, nil
}

func topologyPatternSimilarity(a, b ServiceTopologyPattern) float64 {
	left := map[string]bool{}
	right := map[string]bool{}
	for _, edge := range a.Edges {
		left[edge.Source+">"+edge.Target+":"+edge.Type] = true
	}
	for _, edge := range b.Edges {
		right[edge.Source+">"+edge.Target+":"+edge.Type] = true
	}
	return jaccard(left, right)
}
func jaccard(a, b map[string]bool) float64 {
	if len(a) == 0 && len(b) == 0 {
		return 1
	}
	union, inter := 0, 0
	for key := range a {
		union++
		if b[key] {
			inter++
		}
	}
	for key := range b {
		if !a[key] {
			union++
		}
	}
	if union == 0 {
		return 0
	}
	return float64(inter) / float64(union)
}
