package causal

import (
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/kubepilot-aiops/kubepilot/internal/domain"
	"gopkg.in/yaml.v3"
)

type Matcher struct{ patterns []Pattern }

func NewMatcher(patterns []Pattern) *Matcher {
	copyPatterns := append([]Pattern(nil), patterns...)
	return &Matcher{patterns: copyPatterns}
}
func (m *Matcher) Patterns() []Pattern {
	if m == nil {
		return nil
	}
	return append([]Pattern(nil), m.patterns...)
}

type patternFile struct {
	Patterns []Pattern `yaml:"patterns"`
}

func Load(paths ...string) (*Matcher, error) {
	var all []Pattern
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		var file patternFile
		if err = yaml.Unmarshal(raw, &file); err != nil {
			return nil, err
		}
		if len(file.Patterns) == 0 {
			var one Pattern
			if err = yaml.Unmarshal(raw, &one); err != nil {
				return nil, err
			}
			if one.ID == "" {
				one.ID = one.Cause
			}
			file.Patterns = []Pattern{one}
		}
		all = append(all, file.Patterns...)
	}
	return NewMatcher(all), nil
}

func DefaultMatcher() *Matcher {
	return NewMatcher([]Pattern{
		{ID: "memory-leak", Category: "memory", Cause: "memory_leak", Chain: []string{"memory_growth", "oom_killed", "pod_restart", "error_rate_increase"}, RequiredEvidence: []string{"memory_metric", "kubernetes_event"}, Contradictions: []string{"stable_memory"}, Confidence: .8},
		{ID: "database-unavailable", Category: "database", Cause: "database_unavailable", Chain: []string{"connection_error", "endpoint_unavailable", "request_error"}, RequiredEvidence: []string{"database_error", "kubernetes_event"}, Contradictions: []string{"database_healthy"}, Confidence: .8},
		{ID: "network-timeout", Category: "network", Cause: "network_timeout", Chain: []string{"connection_timeout", "downstream_error", "request_error"}, RequiredEvidence: []string{"trace_error", "log_error"}, Contradictions: []string{"connection_success"}, Confidence: .75},
		{ID: "deployment-regression", Category: "deployment", Cause: "deployment_regression", Chain: []string{"new_revision", "probe_failure", "pod_unready", "request_error"}, RequiredEvidence: []string{"kubernetes_event", "deployment_status"}, Contradictions: []string{"rollout_healthy"}, Confidence: .75},
	})
}

func (m *Matcher) MatchEvidence(evidence []domain.Evidence) []PatternMatch {
	if m == nil {
		return nil
	}
	observed := observedTokens(evidence)
	out := make([]PatternMatch, 0, len(m.patterns))
	for _, pattern := range m.patterns {
		matched := []string{}
		missing := []string{}
		for _, node := range pattern.Chain {
			if observedNode(observed, node) {
				matched = append(matched, node)
			} else {
				missing = append(missing, node)
			}
		}
		coverage := 0.0
		if len(pattern.Chain) > 0 {
			coverage = float64(len(matched)) / float64(len(pattern.Chain))
		}
		contradictions := []string{}
		for _, marker := range pattern.Contradictions {
			if observedNode(observed, marker) {
				contradictions = append(contradictions, marker)
			}
		}
		required := []string{}
		for _, req := range pattern.RequiredEvidence {
			if observedNode(observed, req) {
				required = append(required, req)
			}
		}
		out = append(out, PatternMatch{PatternID: pattern.ID, Category: pattern.Category, Cause: pattern.Cause, CausalPath: matched, Coverage: coverage, MissingNodes: missing, RequiredEvidence: required, Contradictions: contradictions, ContradictionScore: float64(len(contradictions)) / float64(max(1, len(pattern.Contradictions)))})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Coverage == out[j].Coverage {
			return out[i].PatternID < out[j].PatternID
		}
		return out[i].Coverage > out[j].Coverage
	})
	return out
}

func (m *Matcher) Expand(patternID string, observed []domain.Evidence) (PatternMatch, bool) {
	for _, match := range m.MatchEvidence(observed) {
		if match.PatternID == patternID {
			return match, true
		}
	}
	return PatternMatch{}, false
}

func observedTokens(evidence []domain.Evidence) map[string]bool {
	seen := map[string]bool{}
	for _, item := range evidence {
		observation := strings.ToLower(strings.Join([]string{item.Type, item.Kind, item.Summary, stringify(item.Content), stringify(item.Data)}, " "))
		for _, value := range []string{item.Type, item.Kind, item.Summary, stringify(item.Content), stringify(item.Data)} {
			for _, token := range tokenize(value) {
				seen[token] = true
			}
		}
		if item.Source == "kubernetes" && containsAnomaly(observation) {
			seen["kubernetes_event"] = true
		}
		if item.Source == "prometheus" || item.Source == "metric" {
			seen["memory_metric"] = seen["memory_metric"] || strings.Contains(strings.ToLower(item.Summary), "memory")
		}
		if (item.Source == "jaeger" || item.Source == "trace") && containsAnomaly(observation) {
			seen["trace_error"] = true
		}
		if (item.Source == "loki" || item.Source == "log") && containsAnomaly(observation) {
			seen["log_error"] = true
		}
	}
	return seen
}

func containsAnomaly(value string) bool {
	for _, marker := range []string{"error", "failed", "failure", "timeout", "refused", "unavailable", "oom", "unready", "saturated", "throttl", "latency"} {
		if strings.Contains(value, marker) {
			return true
		}
	}
	return false
}

func observedNode(observed map[string]bool, node string) bool {
	normalized := normalizeNode(node)
	if normalized == "" {
		return false
	}
	for token := range observed {
		if strings.Contains(normalizeNode(token), normalized) || strings.Contains(normalized, normalizeNode(token)) {
			return true
		}
	}
	return false
}

func normalizeNode(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.NewReplacer("_", "", "-", "", " ", "", ".", "").Replace(value)
	return value
}

var tokenPattern = regexp.MustCompile(`[a-z][a-z0-9_\-]+`)

func tokenize(value string) []string {
	value = strings.ToLower(value)
	return tokenPattern.FindAllString(value, -1)
}
func stringify(value map[string]any) string {
	if value == nil {
		return ""
	}
	parts := []string{}
	for key, item := range value {
		parts = append(parts, key, strings.ToLower(fmt.Sprint(item)))
	}
	sort.Strings(parts)
	return strings.Join(parts, " ")
}
func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
