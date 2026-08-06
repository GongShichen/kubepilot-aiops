package reasoning

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/kubepilot-aiops/kubepilot/internal/domain"
	evidencepolicy "github.com/kubepilot-aiops/kubepilot/internal/reasoning/evidence"
	"github.com/kubepilot-aiops/kubepilot/internal/safety"
	topologyretrieval "github.com/kubepilot-aiops/kubepilot/retrieval/topology"
)

var (
	dynamicTimestampPattern  = regexp.MustCompile(`\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:?\d{2})`)
	dynamicIdentifierPattern = regexp.MustCompile(`(?i)\b(?:[0-9a-f]{16,}|\d{2,})\b`)
)

type Config struct {
	SemanticTopK          int
	LexicalTopK           int
	TopologyTopK          int
	RRFK                  int
	RerankTopK            int
	ModelEvidenceMaxItems int
	ModelContextMaxBytes  int
	RankingPolicy         *evidencepolicy.Policy
}

func DefaultConfig() Config {
	return Config{SemanticTopK: 50, LexicalTopK: 50, TopologyTopK: 50, RRFK: 60, RerankTopK: 5, ModelEvidenceMaxItems: 12, ModelContextMaxBytes: 32768}
}

type Engine struct {
	config Config
	policy *evidencepolicy.Policy
}

func (e *Engine) SemanticTopK() int { return e.config.SemanticTopK }
func (e *Engine) LexicalTopK() int  { return e.config.LexicalTopK }
func (e *Engine) TopologyTopK() int { return e.config.TopologyTopK }

func New(config Config) *Engine {
	d := DefaultConfig()
	if config.SemanticTopK > 0 {
		d.SemanticTopK = config.SemanticTopK
	}
	if config.LexicalTopK > 0 {
		d.LexicalTopK = config.LexicalTopK
	}
	if config.TopologyTopK > 0 {
		d.TopologyTopK = config.TopologyTopK
	}
	if config.RRFK > 0 {
		d.RRFK = config.RRFK
	}
	if config.RerankTopK > 0 {
		d.RerankTopK = config.RerankTopK
	}
	if config.ModelEvidenceMaxItems > 0 {
		d.ModelEvidenceMaxItems = config.ModelEvidenceMaxItems
	}
	if config.ModelContextMaxBytes > 0 {
		d.ModelContextMaxBytes = config.ModelContextMaxBytes
	}
	return &Engine{config: d, policy: config.RankingPolicy}
}

type RankedEvidence struct {
	Evidence []domain.Evidence      `json:"evidence"`
	Ledger   domain.DiagnosisLedger `json:"ledger"`
}

func (e *Engine) RankEvidence(incident *domain.Incident, input []domain.Evidence) (RankedEvidence, error) {
	if incident == nil {
		return RankedEvidence{}, fmt.Errorf("incident is required")
	}
	ledger := domain.DiagnosisLedger{EvidenceOriginalCount: len(input)}
	original, _ := json.Marshal(input)
	ledger.EvidenceOriginalBytes = len(original)
	evidenceContextItems.WithLabelValues("before").Observe(float64(ledger.EvidenceOriginalCount))
	evidenceContextBytes.WithLabelValues("before").Observe(float64(ledger.EvidenceOriginalBytes))
	now := time.Now().UTC()
	start := incident.EvidenceStartAt
	if start.IsZero() {
		start = now.Add(-5 * time.Minute)
	}
	end := now
	dedup := make(map[string]domain.Evidence, len(input))
	for _, item := range input {
		if item.ID == "" {
			continue
		}
		if !insideWindow(item, start, end) {
			continue
		}
		item.RelevanceScore, item.RankingReasons = evidenceScore(incident, item, start, end)
		if previous, exists := dedup[item.ID]; !exists || item.RelevanceScore > previous.RelevanceScore {
			dedup[item.ID] = item
		}
	}
	items := make([]domain.Evidence, 0, len(dedup))
	for _, item := range dedup {
		items = append(items, item)
	}
	if e.policy != nil {
		items = evidencepolicy.Rank(*e.policy, incident, items)
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].RelevanceScore == items[j].RelevanceScore {
			return items[i].ID < items[j].ID
		}
		return items[i].RelevanceScore > items[j].RelevanceScore
	})
	selected := preserveRequiredSources(items, e.config.ModelEvidenceMaxItems)
	for index := range selected {
		selected[index].RankingReasons = append(selected[index].RankingReasons, "retained_for_model_context")
		selected[index] = evidenceForModelContext(selected[index])
	}
	selected = fitEvidenceBytes(selected, e.config.ModelContextMaxBytes)
	if !hasSource(selected, "kubernetes") {
		return RankedEvidence{}, fmt.Errorf("ranked evidence is missing Kubernetes evidence")
	}
	if !hasAnySource(selected, "metric", "prometheus", "log", "loki", "trace", "jaeger") {
		return RankedEvidence{}, fmt.Errorf("ranked evidence is missing metric, log, or trace evidence")
	}
	retained, _ := json.Marshal(selected)
	ledger.EvidenceRetainedCount = len(selected)
	ledger.EvidenceRetainedBytes = len(retained)
	evidenceContextItems.WithLabelValues("after").Observe(float64(ledger.EvidenceRetainedCount))
	evidenceContextBytes.WithLabelValues("after").Observe(float64(ledger.EvidenceRetainedBytes))
	return RankedEvidence{Evidence: selected, Ledger: ledger}, nil
}

func evidenceForModelContext(item domain.Evidence) domain.Evidence {
	content := item.Content
	if content == nil {
		content = item.Data
	}
	if content != nil {
		clean := make(map[string]any, len(content))
		for key, value := range content {
			// Query text is server-generated provenance and often dominates metric
			// payload size; the metric type and returned result retain the observed
			// fact used for diagnosis.
			if key == "query" {
				continue
			}
			clean[key] = value
		}
		content = clean
	}
	item.Content = content
	item.Data = nil
	return item
}

func insideWindow(e domain.Evidence, start, end time.Time) bool {
	t := e.Timestamp
	if t.IsZero() {
		t = e.ObservedAt
	}
	if t.IsZero() {
		t = e.CollectedAt
	}
	if t.IsZero() {
		return true
	}
	return !t.Before(start) && !t.After(end.Add(30*time.Second))
}

func evidenceScore(in *domain.Incident, e domain.Evidence, start, end time.Time) (float64, []string) {
	window := .5
	t := e.Timestamp
	if t.IsZero() {
		t = e.ObservedAt
	}
	if !t.IsZero() {
		target := in.CreatedAt
		if target.IsZero() || target.Before(start) || target.After(end) {
			target = end
		}
		span := math.Max(float64(end.Sub(start)), 1)
		window = clamp(1 - math.Abs(float64(t.Sub(target)))/span)
	}
	match := 0.0
	if e.Namespace == "" || e.Namespace == in.Namespace {
		match += .35
	}
	if e.Service == in.Service {
		match += .4
	} else if e.Service == "" {
		match += .15
	}
	if e.Resource == in.Resource && e.Resource != "" {
		match += .25
	}
	correlation := 0.0
	if e.TraceID != "" && (in.TraceID == "" || e.TraceID == in.TraceID) {
		correlation += .4
	}
	text := strings.ToLower(e.Summary + " " + stringify(e.Content) + " " + stringify(e.Data))
	if in.Resource != "" && strings.Contains(text, strings.ToLower(in.Resource)) {
		correlation += .3
	}
	if in.Service != "" && strings.Contains(text, strings.ToLower(in.Service)) {
		correlation += .3
	}
	observationText := text
	if sourceIn(e.Source, "metric", "prometheus") {
		// Query names describe what was inspected, not what was observed. Scoring
		// only the returned value prevents a healthy throttling query from being
		// treated as throttling evidence merely because its PromQL contains that
		// word.
		observationText = strings.ToLower(stringify(observedResult(e.Content, e.Data)))
	}
	discriminative := discriminativeness(observationText)
	quality := map[string]float64{"kubernetes": 1, "prometheus": .95, "metric": .95, "jaeger": .9, "trace": .9, "loki": .85, "log": .85, "historical": .65}[strings.ToLower(e.Source)]
	if quality == 0 {
		quality = .5
	}
	rarityText := text
	if sourceIn(e.Source, "metric", "prometheus") {
		rarityText = observationText
	}
	rarity := severityRarity(e, rarityText)
	score := .25*window + .20*clamp(match) + .20*clamp(correlation) + .15*discriminative + .10*quality + .10*clamp(rarity)
	reasons := []string{fmt.Sprintf("window=%.3f", window), fmt.Sprintf("resource_match=%.3f", clamp(match)), fmt.Sprintf("correlation=%.3f", clamp(correlation)), fmt.Sprintf("discriminativeness=%.3f", discriminative), fmt.Sprintf("source_quality=%.3f", quality), fmt.Sprintf("severity_rarity=%.3f", clamp(rarity))}
	return score, reasons
}

func discriminativeness(text string) float64 {
	markers := []string{"oomkilled", "crashloopbackoff", "imagepullbackoff", "connection refused", "timeout", "throttl", "probe", "selector", "revision", "credential", "lock wait", "endpoint"}
	hits := 0
	for _, marker := range markers {
		if strings.Contains(text, marker) {
			hits++
		}
	}
	return clamp(float64(hits)/3 + .2)
}

func observedResult(maps ...map[string]any) any {
	for _, values := range maps {
		if result, ok := values["result"]; ok {
			return result
		}
	}
	return nil
}

func severityRarity(e domain.Evidence, text string) float64 {
	level := strings.ToLower(findString(e.Content, "level", "severity"))
	if level == "" {
		level = strings.ToLower(findString(e.Data, "level", "severity"))
	}
	if level == "" {
		for _, candidate := range []string{"critical", "fatal", "error", "warn", "info", "debug"} {
			if strings.Contains(text, `"level":"`+candidate+`"`) || strings.Contains(text, "level="+candidate) {
				level = candidate
				break
			}
		}
	}
	score := map[string]float64{"critical": 1, "fatal": 1, "error": .9, "warn": .55, "warning": .55, "info": .08, "debug": .02}[level]
	if level == "" {
		score = .3
	}
	if count, ok := numeric(firstValue(e.Content, "occurrence_count")); ok {
		switch {
		case count <= 2:
			score = math.Max(score, .9)
		case count <= 10:
			score = math.Max(score, .7)
		case count <= 100:
			score = math.Max(score, .45)
		}
	}
	if containsFailureMarker(text) {
		score = math.Max(score, .8)
	}
	return clamp(score)
}

func firstValue(values map[string]any, keys ...string) any {
	for _, key := range keys {
		if value, ok := values[key]; ok {
			return value
		}
	}
	return nil
}

func containsFailureMarker(value string) bool {
	value = strings.ToLower(value)
	for _, marker := range []string{"error", "exception", "timeout", "killed", "failed", "failure", "oom", "refused", "unavailable", "throttl", "crashloop", "imagepull", "probe"} {
		if strings.Contains(value, marker) {
			return true
		}
	}
	return false
}

func preserveRequiredSources(items []domain.Evidence, maxItems int) []domain.Evidence {
	if maxItems <= 0 {
		return nil
	}
	chosen := map[string]bool{}
	signatures := map[string]bool{}
	metricFamilies := map[string]bool{}
	out := make([]domain.Evidence, 0, min(maxItems, len(items)))
	appendBest := func(reason string, match func(domain.Evidence) bool) {
		if len(out) >= maxItems {
			return
		}
		for _, item := range items {
			signature := evidenceSignature(item)
			if !chosen[item.ID] && !signatures[signature] && match(item) {
				chosen[item.ID] = true
				signatures[signature] = true
				if family := metricEvidenceFamily(item); family != "" {
					metricFamilies[family] = true
				}
				item.RankingReasons = append(item.RankingReasons, reason)
				out = append(out, item)
				return
			}
		}
	}
	appendBest("required_kubernetes_source", func(v domain.Evidence) bool { return strings.EqualFold(v.Source, "kubernetes") })
	// A single high-scoring telemetry modality must not crowd out the other
	// independent observations. Each available modality is therefore represented
	// before relevance ranking fills the remaining context budget.
	for _, modality := range []string{"metric", "log", "trace"} {
		modality := modality
		appendBest("required_"+modality+"_source", func(v domain.Evidence) bool {
			return evidenceModality(v) == modality
		})
	}
	// Prometheus-style collections commonly contain a point-in-time and a
	// current-window view of the same signal. Keep one of each signal family
	// before admitting duplicate views so load, latency, error, resource and
	// availability observations remain independently inspectable.
	for _, item := range items {
		if len(out) >= maxItems {
			break
		}
		family := metricEvidenceFamily(item)
		if family == "" || metricFamilies[family] {
			continue
		}
		signature := evidenceSignature(item)
		if chosen[item.ID] || signatures[signature] {
			continue
		}
		chosen[item.ID] = true
		signatures[signature] = true
		metricFamilies[family] = true
		item.RankingReasons = append(item.RankingReasons, "retained_metric_signal_family")
		out = append(out, item)
	}
	for _, item := range items {
		if len(out) >= maxItems {
			break
		}
		signature := evidenceSignature(item)
		if !chosen[item.ID] && !signatures[signature] {
			chosen[item.ID] = true
			signatures[signature] = true
			out = append(out, item)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].RelevanceScore == out[j].RelevanceScore {
			return out[i].ID < out[j].ID
		}
		return out[i].RelevanceScore > out[j].RelevanceScore
	})
	return out
}

func evidenceModality(item domain.Evidence) string {
	switch strings.ToLower(strings.TrimSpace(item.Source)) {
	case "metric", "prometheus":
		return "metric"
	case "log", "loki":
		return "log"
	case "trace", "jaeger":
		return "trace"
	case "kubernetes":
		return "topology"
	default:
		return ""
	}
}

func metricEvidenceFamily(item domain.Evidence) string {
	if evidenceModality(item) != "metric" {
		return ""
	}
	kind := strings.ToLower(strings.TrimSpace(firstNonEmpty(item.Kind, item.Type)))
	if kind == "" {
		return ""
	}
	for _, suffix := range []string{"_current", "_trend"} {
		kind = strings.TrimSuffix(kind, suffix)
	}
	return kind
}

func evidenceSignature(item domain.Evidence) string {
	summary := canonicalEvidenceSummary(item.Summary)
	summary = dynamicTimestampPattern.ReplaceAllString(summary, "<timestamp>")
	summary = dynamicIdentifierPattern.ReplaceAllString(summary, "<value>")
	summary = strings.Join(strings.Fields(summary), " ")
	return strings.ToLower(item.Source + "\x00" + firstNonEmpty(item.Type, item.Kind) + "\x00" + summary)
}

func canonicalEvidenceSummary(summary string) string {
	var value any
	if json.Unmarshal([]byte(summary), &value) == nil {
		value = removeDynamicFields(value)
		if normalized, err := json.Marshal(value); err == nil {
			return strings.ToLower(string(normalized))
		}
	}
	return strings.ToLower(strings.TrimSpace(summary))
}

func removeDynamicFields(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			switch strings.ToLower(key) {
			case "time", "timestamp", "trace_id", "request_id", "user_id", "order_id", "pod", "pod_name":
				out[key] = "<dynamic>"
			default:
				out[key] = removeDynamicFields(item)
			}
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for i := range typed {
			out[i] = removeDynamicFields(typed[i])
		}
		return out
	default:
		return value
	}
}

func fitEvidenceBytes(items []domain.Evidence, maxBytes int) []domain.Evidence {
	if maxBytes <= 0 {
		return nil
	}
	result := append([]domain.Evidence(nil), items...)
	for len(result) > 0 {
		raw, _ := json.Marshal(result)
		if len(raw) <= maxBytes {
			return result
		}
		largest := -1
		largestBytes := 0
		for i := range result {
			b, _ := json.Marshal(result[i])
			if len(b) > largestBytes {
				largest, largestBytes = i, len(b)
			}
		}
		if largest < 0 {
			break
		}
		budget := max(128, largestBytes-(len(raw)-maxBytes)-64)
		if !hasReasonPrefix(result[largest].RankingReasons, "original_payload_bytes=") {
			result[largest].RankingReasons = append(result[largest].RankingReasons, fmt.Sprintf("original_payload_bytes=%d", largestBytes))
		}
		result[largest].Summary = truncateUTF8(result[largest].Summary, budget/3)
		result[largest].Content = truncateMap(result[largest].Content, budget/3)
		result[largest].Data = truncateMap(result[largest].Data, budget/3)
		if !hasReasonPrefix(result[largest].RankingReasons, "payload_truncated") {
			result[largest].RankingReasons = append(result[largest].RankingReasons, "payload_truncated")
		}
		updated, _ := json.Marshal(result[largest])
		if len(updated) >= largestBytes && len(result) > 2 {
			result = append(result[:largest], result[largest+1:]...)
		}
	}
	return result
}

func hasReasonPrefix(reasons []string, prefix string) bool {
	for _, reason := range reasons {
		if strings.HasPrefix(reason, prefix) {
			return true
		}
	}
	return false
}

func truncateMap(in map[string]any, maxBytes int) map[string]any {
	if len(in) == 0 {
		return in
	}
	raw, _ := json.Marshal(in)
	if len(raw) <= maxBytes {
		return in
	}
	return map[string]any{"truncated": truncateUTF8(string(raw), maxBytes)}
}

func truncateUTF8(value string, maxBytes int) string {
	if len(value) <= maxBytes {
		return value
	}
	if maxBytes <= 0 {
		return ""
	}
	value = value[:maxBytes]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}

func (e *Engine) BuildFeatures(incident *domain.Incident, evidence []domain.Evidence) domain.IncidentFeatures {
	f := domain.IncidentFeatures{IncidentID: incident.ID, Cluster: incident.Cluster, Namespace: incident.Namespace, Service: incident.Service, Resource: incident.Resource, WindowStart: incident.EvidenceStartAt, WindowEnd: time.Now().UTC(), Observed: map[string]float64{}}
	terms, types, traces, templates, topology, causal := map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, item := range evidence {
		types[firstNonEmpty(item.Type, item.Kind)] = true
		if item.TraceID != "" {
			traces[item.TraceID] = true
		}
		if item.TemplateID != "" {
			templates[item.TemplateID] = true
		}
		if item.Service != "" {
			topology[item.Service] = true
		}
		for _, service := range InferTopologyServices(item.Summary, stringify(item.Content), stringify(item.Data)) {
			topology[service] = true
		}
		for _, id := range item.CausalNodeIDs {
			causal[id] = true
		}
		featureText := item.Summary + " " + stringify(item.Content) + " " + stringify(item.Data)
		if sourceIn(item.Source, "metric", "prometheus") {
			featureText = item.Summary + " " + stringify(observedResult(item.Content, item.Data))
		}
		for _, term := range tokenize(featureText) {
			terms[term] = true
		}
		for _, values := range []map[string]any{item.Content, item.Data} {
			for key, value := range values {
				if key == "query" {
					continue
				}
				if number, ok := numeric(value); ok {
					f.Observed[key] = number
				}
			}
		}
		if revision := findString(item.Content, "revision", "deployment_revision"); revision != "" {
			f.Revision = revision
		} else if revision = findString(item.Data, "revision", "deployment_revision"); revision != "" {
			f.Revision = revision
		}
	}
	f.Terms, f.EvidenceTypes, f.TraceIDs, f.TemplateIDs, f.TopologyServices, f.CausalNodeIDs = keys(terms), keys(types), keys(traces), keys(templates), keys(topology), keys(causal)
	f.TopologyGraph = BuildIncidentDependencyGraph(incident, evidence, f.TopologyServices)
	return f
}

// BuildIncidentDependencyGraph converts observed trace, endpoint and runtime
// metadata into a deterministic dependency graph. It intentionally consumes
// only evidence supplied by the observability collectors; no benchmark labels
// or model-generated root cause is consulted.
func BuildIncidentDependencyGraph(incident *domain.Incident, evidence []domain.Evidence, services []string) domain.IncidentDependencyGraph {
	graph := domain.IncidentDependencyGraph{}
	if incident == nil {
		return graph
	}
	graph.RootService = incident.Service
	nodes := map[string]domain.DependencyNode{}
	addNode := func(id, kind, service, role string) {
		if strings.TrimSpace(id) == "" {
			return
		}
		if _, exists := nodes[id]; !exists {
			nodes[id] = domain.DependencyNode{ID: id, Kind: kind, Service: service, Role: role}
		}
	}
	addNode(incident.Service, "service", incident.Service, "root")
	for _, service := range services {
		role := "dependency"
		kind := "service"
		switch strings.ToLower(service) {
		case "mysql", "postgres", "postgresql", "redis", "kafka", "database":
			role, kind = "critical_dependency", "datastore"
		}
		addNode(service, kind, service, role)
	}
	edges := map[string]domain.DependencyEdge{}
	paths := map[string][]string{}
	addEdge := func(from, to, kind string, evidence domain.Evidence) {
		if from == "" || to == "" || from == to {
			return
		}
		key := from + ">" + to + ":" + kind
		edge := domain.DependencyEdge{From: from, To: to, Kind: kind}
		if value, ok := numeric(evidence.Content["latency_ms"]); ok {
			edge.LatencyMS = value
		}
		if value, ok := numeric(evidence.Content["error_rate"]); ok {
			edge.ErrorRate = value
		}
		edges[key] = edge
		paths[from+">"+to] = []string{from, to}
	}
	for _, item := range evidence {
		text := strings.ToLower(item.Summary + " " + stringify(item.Content) + " " + stringify(item.Data))
		observed := InferTopologyServices(item.Summary, stringify(item.Content), stringify(item.Data))
		from := firstNonEmpty(item.Service, incident.Service)
		for _, dependency := range observed {
			if dependency == from {
				continue
			}
			addNode(from, "service", from, "service")
			role, kind := "dependency", "service"
			if dependency == "mysql" || dependency == "postgres" || dependency == "postgresql" || dependency == "redis" || dependency == "database" {
				role, kind = "critical_dependency", "datastore"
			}
			addNode(dependency, kind, dependency, role)
			addEdge(from, dependency, "observed_call", item)
			if strings.Contains(text, "error") || strings.Contains(text, "timeout") || strings.Contains(text, "refused") || strings.Contains(text, "failed") {
				graph.SuspectedFailureNodes = append(graph.SuspectedFailureNodes, dependency)
			}
		}
		for _, key := range []string{"upstream_service", "downstream_service", "dependency", "target_service", "endpoint_service"} {
			dependency := findString(item.Content, key)
			if dependency == "" {
				dependency = findString(item.Data, key)
			}
			if dependency != "" {
				addNode(dependency, "service", dependency, "dependency")
				addEdge(from, dependency, key, item)
			}
		}
	}
	for _, node := range nodes {
		graph.Nodes = append(graph.Nodes, node)
	}
	for _, edge := range edges {
		graph.Edges = append(graph.Edges, edge)
	}
	sort.Slice(graph.Nodes, func(i, j int) bool { return graph.Nodes[i].ID < graph.Nodes[j].ID })
	sort.Slice(graph.Edges, func(i, j int) bool {
		if graph.Edges[i].From == graph.Edges[j].From {
			return graph.Edges[i].To < graph.Edges[j].To
		}
		return graph.Edges[i].From < graph.Edges[j].From
	})
	graph.SuspectedFailureNodes = uniqueStrings(graph.SuspectedFailureNodes)
	for _, path := range paths {
		graph.ErrorPropagationPaths = append(graph.ErrorPropagationPaths, path)
	}
	sort.Slice(graph.ErrorPropagationPaths, func(i, j int) bool {
		return strings.Join(graph.ErrorPropagationPaths[i], ">") < strings.Join(graph.ErrorPropagationPaths[j], ">")
	})
	return graph
}

func InferTopologyServices(values ...string) []string {
	out := map[string]bool{}
	for _, value := range values {
		for _, token := range tokenize(value) {
			normalized := strings.Trim(token, "-_")
			if strings.HasSuffix(normalized, "-service") || strings.HasSuffix(normalized, "_service") {
				out[normalized] = true
				continue
			}
			switch normalized {
			case "mysql", "redis", "postgres", "postgresql", "database", "kafka", "jaeger", "prometheus", "loki":
				out[normalized] = true
			}
		}
	}
	return keys(out)
}

func (e *Engine) AnnotateCausalNodes(evidence []domain.Evidence, patterns []domain.CausalPattern) []domain.Evidence {
	out := append([]domain.Evidence(nil), evidence...)
	for index := range out {
		matched := map[string]bool{}
		for _, id := range out[index].CausalNodeIDs {
			matched[id] = true
		}
		text := strings.ToLower(out[index].Summary + " " + stringify(out[index].Content) + " " + stringify(out[index].Data))
		for _, pattern := range patterns {
			if pattern.Status != "active" {
				continue
			}
			for _, node := range pattern.Nodes {
				for _, token := range node.Match {
					if strings.Contains(text, strings.ToLower(token)) {
						matched[node.ID] = true
						break
					}
				}
			}
		}
		out[index].CausalNodeIDs = keys(matched)
	}
	return out
}

type CandidateLists struct {
	Semantic []domain.RetrievalCandidate `json:"semantic"`
	Lexical  []domain.RetrievalCandidate `json:"lexical"`
	Topology []domain.RetrievalCandidate `json:"topology"`
}

func (e *Engine) Fuse(input CandidateLists) []domain.RetrievalCandidate {
	retrievalCandidateCount.WithLabelValues("semantic").Observe(float64(len(input.Semantic)))
	retrievalCandidateCount.WithLabelValues("lexical").Observe(float64(len(input.Lexical)))
	retrievalCandidateCount.WithLabelValues("topology").Observe(float64(len(input.Topology)))
	// Candidate generation is intentionally limited to dense and lexical
	// recall. Topology is a reasoning feature and is applied by the retrieval
	// pipeline after this stage; accepting it here would make sparse topology
	// knowledge a hard recall filter.
	lists := map[string][]domain.RetrievalCandidate{"semantic": input.Semantic, "lexical": input.Lexical}
	merged := map[string]domain.RetrievalCandidate{}
	for _, source := range []string{"semantic", "lexical"} {
		list := lists[source]
		for index, item := range list {
			if item.IncidentID == "" {
				continue
			}
			current, ok := merged[item.IncidentID]
			if !ok {
				current = item
				current.SourceRanks = map[string]int{}
				current.SourceScores = map[string]float64{}
			} else {
				current = mergeCandidate(current, item)
			}
			current.SourceRanks[source] = index + 1
			score := item.SourceScores[source]
			if score <= 0 {
				score = 1 / float64(index+1)
			}
			if score > 1 {
				score = 1
			}
			current.SourceScores[source] = score
			if source == "semantic" {
				current.Rank.SemanticScore = score
			} else {
				current.Rank.LexicalScore = score
			}
			merged[item.IncidentID] = current
		}
	}
	out := make([]domain.RetrievalCandidate, 0, len(merged))
	for _, item := range merged {
		item.Rank.DeterministicScore = .6*item.Rank.SemanticScore + .4*item.Rank.LexicalScore
		item.Rank.FinalScore = item.Rank.DeterministicScore
		item.RankingReasons = []string{fmt.Sprintf("candidate_generation=0.6*semantic(%.4f)+0.4*lexical(%.4f)", item.Rank.SemanticScore, item.Rank.LexicalScore)}
		out = append(out, item)
	}
	sortCandidates(out)
	if len(out) > 100 {
		out = out[:100]
	}
	retrievalCandidateCount.WithLabelValues("fusion").Observe(float64(len(out)))
	return out
}

func mergeCandidate(current, incoming domain.RetrievalCandidate) domain.RetrievalCandidate {
	if current.Namespace == "" {
		current.Namespace = incoming.Namespace
	}
	if current.Service == "" {
		current.Service = incoming.Service
	}
	if current.Resource == "" {
		current.Resource = incoming.Resource
	}
	if current.Category == "" {
		current.Category = incoming.Category
	}
	if current.RootCause == "" {
		current.RootCause = incoming.RootCause
	}
	if current.Summary == "" {
		current.Summary = incoming.Summary
	}
	if current.Revision == "" {
		current.Revision = incoming.Revision
	}
	current.Features.Terms = mergeStrings(current.Features.Terms, incoming.Features.Terms)
	current.Features.EvidenceTypes = mergeStrings(current.Features.EvidenceTypes, incoming.Features.EvidenceTypes)
	current.Features.TraceIDs = mergeStrings(current.Features.TraceIDs, incoming.Features.TraceIDs)
	current.Features.TemplateIDs = mergeStrings(current.Features.TemplateIDs, incoming.Features.TemplateIDs)
	current.Features.TopologyServices = mergeStrings(current.Features.TopologyServices, incoming.Features.TopologyServices)
	current.Features.TopologyGraph = mergeTopologyGraphs(current.Features.TopologyGraph, incoming.Features.TopologyGraph)
	current.Features.CausalNodeIDs = mergeStrings(current.Features.CausalNodeIDs, incoming.Features.CausalNodeIDs)
	if current.Features.Observed == nil {
		current.Features.Observed = map[string]float64{}
	}
	for key, value := range incoming.Features.Observed {
		current.Features.Observed[key] = value
	}
	return current
}

func mergeTopologyGraphs(left, right domain.IncidentDependencyGraph) domain.IncidentDependencyGraph {
	out := left
	if out.RootService == "" {
		out.RootService = right.RootService
	}
	nodes := map[string]domain.DependencyNode{}
	for _, node := range append(append([]domain.DependencyNode(nil), left.Nodes...), right.Nodes...) {
		if node.ID == "" {
			continue
		}
		if previous, exists := nodes[node.ID]; exists {
			if previous.Kind == "" {
				previous.Kind = node.Kind
			}
			if previous.Service == "" {
				previous.Service = node.Service
			}
			if previous.Role == "" {
				previous.Role = node.Role
			}
			if previous.Resource == "" {
				previous.Resource = node.Resource
			}
			if previous.Metadata == nil {
				previous.Metadata = map[string]string{}
			}
			for key, value := range node.Metadata {
				previous.Metadata[key] = value
			}
			nodes[node.ID] = previous
			continue
		}
		nodes[node.ID] = node
	}
	out.Nodes = out.Nodes[:0]
	for _, node := range nodes {
		out.Nodes = append(out.Nodes, node)
	}
	edges := map[string]domain.DependencyEdge{}
	for _, edge := range append(append([]domain.DependencyEdge(nil), left.Edges...), right.Edges...) {
		edges[edge.From+">"+edge.To+":"+edge.Kind] = edge
	}
	out.Edges = out.Edges[:0]
	for _, edge := range edges {
		out.Edges = append(out.Edges, edge)
	}
	out.SuspectedFailureNodes = mergeStrings(left.SuspectedFailureNodes, right.SuspectedFailureNodes)
	paths := map[string][]string{}
	for _, path := range append(append([][]string(nil), left.ErrorPropagationPaths...), right.ErrorPropagationPaths...) {
		paths[strings.Join(path, ">")] = path
	}
	out.ErrorPropagationPaths = out.ErrorPropagationPaths[:0]
	for _, path := range paths {
		out.ErrorPropagationPaths = append(out.ErrorPropagationPaths, path)
	}
	sort.Slice(out.Nodes, func(i, j int) bool { return out.Nodes[i].ID < out.Nodes[j].ID })
	sort.Slice(out.Edges, func(i, j int) bool {
		if out.Edges[i].From == out.Edges[j].From {
			return out.Edges[i].To < out.Edges[j].To
		}
		return out.Edges[i].From < out.Edges[j].From
	})
	sort.Slice(out.ErrorPropagationPaths, func(i, j int) bool {
		return strings.Join(out.ErrorPropagationPaths[i], ">") < strings.Join(out.ErrorPropagationPaths[j], ">")
	})
	return out
}

func mergeStrings(left, right []string) []string {
	values := map[string]bool{}
	for _, value := range append(append([]string(nil), left...), right...) {
		if value != "" {
			values[value] = true
		}
	}
	return keys(values)
}

func (e *Engine) Rerank(features domain.IncidentFeatures, candidates []domain.RetrievalCandidate) []domain.RetrievalCandidate {
	out := append([]domain.RetrievalCandidate(nil), candidates...)
	for i := range out {
		candidate := &out[i]
		candidate.Rank.TopologySimilarity = topologySimilarity(features, candidate.Features)
		candidate.Rank.EvidenceFeatureOverlap = jaccard(features.Terms, candidate.Features.Terms)
		service := 0.0
		if candidate.Namespace == features.Namespace {
			service += .25
		}
		if candidate.Service == features.Service {
			service += .5
		}
		if candidate.Resource != "" && candidate.Resource == features.Resource {
			service += .25
		}
		candidate.Rank.ServiceResourceProximity = clamp(service)
		candidate.Rank.CausalPathCoverage = jaccard(features.CausalNodeIDs, candidate.Features.CausalNodeIDs)
		revision := 0.0
		if features.Revision != "" && candidate.Revision == features.Revision {
			revision = 1
		} else if candidate.Namespace == features.Namespace {
			revision = .3
		}
		candidate.Rank.RevisionTemporalContext = revision
		candidate.Rank.FinalScore = .30*candidate.Rank.NormalizedRRF + .20*candidate.Rank.TopologySimilarity + .15*candidate.Rank.EvidenceFeatureOverlap + .15*candidate.Rank.ServiceResourceProximity + .10*candidate.Rank.CausalPathCoverage + .10*candidate.Rank.RevisionTemporalContext
		candidate.Rank.DeterministicScore = candidate.Rank.FinalScore
		rankingScore.WithLabelValues("rrf").Observe(candidate.Rank.NormalizedRRF)
		rankingScore.WithLabelValues("rerank_final").Observe(candidate.Rank.FinalScore)
		candidate.RankingReasons = []string{fmt.Sprintf("weighted_rrf=%.4f", candidate.Rank.NormalizedRRF), fmt.Sprintf("topology_graph_similarity=%.4f", candidate.Rank.TopologySimilarity), fmt.Sprintf("evidence_overlap=%.4f", candidate.Rank.EvidenceFeatureOverlap), fmt.Sprintf("service_resource=%.4f", candidate.Rank.ServiceResourceProximity), fmt.Sprintf("causal_coverage=%.4f", candidate.Rank.CausalPathCoverage), fmt.Sprintf("revision_temporal=%.4f", candidate.Rank.RevisionTemporalContext)}
	}
	if e.policy != nil {
		out = evidencepolicy.RankCandidates(*e.policy, out)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Rank.FinalScore == out[j].Rank.FinalScore {
			return out[i].IncidentID < out[j].IncidentID
		}
		return out[i].Rank.FinalScore > out[j].Rank.FinalScore
	})
	if len(out) > e.config.RerankTopK {
		out = out[:e.config.RerankTopK]
	}
	return out
}

// topologySimilarity prefers the observed dependency graph whenever both
// sides contain graph signal.  The service-set fallback is retained for
// legacy knowledge rows that predate graph persistence; it is deliberately
// not used when a graph is available.
func topologySimilarity(current, candidate domain.IncidentFeatures) float64 {
	if hasTopologyGraph(current.TopologyGraph) && hasTopologyGraph(candidate.TopologyGraph) {
		return topologyretrieval.GraphCandidateScore(current.TopologyGraph, candidate.TopologyGraph)
	}
	return jaccard(current.TopologyServices, candidate.TopologyServices)
}

func hasTopologyGraph(graph domain.IncidentDependencyGraph) bool {
	return len(graph.Edges) > 0 || len(graph.ErrorPropagationPaths) > 0 || len(graph.SuspectedFailureNodes) > 0
}

func (e *Engine) MatchCausalPatterns(features domain.IncidentFeatures, evidence []domain.Evidence, patterns []domain.CausalPattern) []domain.CausalPattern {
	text := strings.Join(features.Terms, " ")
	for _, item := range evidence {
		text += " " + strings.ToLower(item.Summary+" "+stringify(item.Content)+" "+stringify(item.Data))
	}
	type scored struct {
		pattern  domain.CausalPattern
		coverage float64
	}
	matched := make([]scored, 0, len(patterns))
	for _, pattern := range patterns {
		if pattern.Status != "active" {
			continue
		}
		hit := 0
		for _, node := range pattern.Nodes {
			for _, token := range node.Match {
				if strings.Contains(text, strings.ToLower(token)) {
					hit++
					break
				}
			}
		}
		coverage := 0.0
		if len(pattern.Nodes) > 0 {
			coverage = float64(hit) / float64(len(pattern.Nodes))
		}
		if coverage > 0 {
			p := pattern
			p.Confidence = clamp(pattern.Confidence * coverage)
			matched = append(matched, scored{p, coverage})
		}
	}
	sort.SliceStable(matched, func(i, j int) bool {
		if matched[i].coverage == matched[j].coverage {
			return matched[i].pattern.ID < matched[j].pattern.ID
		}
		return matched[i].coverage > matched[j].coverage
	})
	out := make([]domain.CausalPattern, 0, len(matched))
	for _, item := range matched {
		out = append(out, item.pattern)
	}
	return out
}

func (e *Engine) VerifyHypotheses(drafts []domain.HypothesisDraft, evidence []domain.Evidence, candidates []domain.RetrievalCandidate, patterns []domain.CausalPattern) ([]domain.VerifiedHypothesis, error) {
	evidence = e.AnnotateCausalNodes(evidence, patterns)
	allowed := map[string]domain.Evidence{}
	for _, item := range evidence {
		allowed[item.ID] = item
	}
	out := make([]domain.VerifiedHypothesis, 0, len(drafts))
	for _, draft := range drafts {
		if len(draft.SupportingEvidenceIDs) == 0 {
			return nil, fmt.Errorf("hypothesis %s has no supporting evidence", draft.ID)
		}
		verified := domain.VerifiedHypothesis{Draft: draft}
		seenSource := map[string]bool{}
		for _, id := range draft.SupportingEvidenceIDs {
			item, ok := allowed[id]
			if !ok {
				return nil, fmt.Errorf("hypothesis %s references unknown or expired evidence ID %s", draft.ID, id)
			}
			verified.VerifiedEvidenceIDs = append(verified.VerifiedEvidenceIDs, id)
			seenSource[item.Source] = true
			verified.SupportingScore += item.RelevanceScore
			if strings.EqualFold(item.Source, "kubernetes") {
				// Topology confidence is primarily a property of the current
				// incident. Historical candidates may strengthen it below, but a
				// strategy without long-term memory must still receive credit for
				// server-observed workload identity.
				topologyScore := .5
				if draft.Service != "" && item.Service == draft.Service {
					topologyScore += .3
				}
				if draft.Resource != "" && item.Resource == draft.Resource {
					topologyScore += .2
				}
				verified.TopologyRelevance = math.Max(verified.TopologyRelevance, clamp(topologyScore))
			}
		}
		verified.SupportingScore = clamp(verified.SupportingScore / float64(len(draft.SupportingEvidenceIDs)))
		for _, id := range draft.ContradictingEvidenceIDs {
			if _, ok := allowed[id]; !ok {
				return nil, fmt.Errorf("hypothesis %s references unknown or expired contradiction evidence ID %s", draft.ID, id)
			}
			verified.ContradictionScore += .25
		}
		verified.ContradictionScore = clamp(verified.ContradictionScore)
		matchedNodes := map[string]bool{}
		for _, item := range evidence {
			for _, node := range item.CausalNodeIDs {
				matchedNodes[node] = true
			}
			summary := strings.ToLower(item.Summary + " " + stringify(item.Content))
			for _, node := range draft.ExpectedCausalPath {
				if strings.Contains(summary, strings.ToLower(node)) || matchedNodes[normalizeCausalNodeID(node)] {
					matchedNodes[node] = true
				}
			}
		}
		for _, node := range draft.ExpectedCausalPath {
			if matchedNodes[node] || matchedNodes[normalizeCausalNodeID(node)] || expectedNodeObserved(node, draft.Category, matchedNodes, patterns) {
				continue
			}
			if !matchedNodes[node] {
				verified.MissingCausalNodes = append(verified.MissingCausalNodes, node)
			}
		}
		if len(draft.ExpectedCausalPath) > 0 {
			verified.CausalPathCoverage = 1 - float64(len(verified.MissingCausalNodes))/float64(len(draft.ExpectedCausalPath))
		}
		for _, candidate := range candidates {
			if candidate.Category == draft.Category {
				verified.HistoricalRelevance = math.Max(verified.HistoricalRelevance, candidate.Rank.FinalScore)
			}
			if candidate.Service == draft.Service {
				verified.TopologyRelevance = math.Max(verified.TopologyRelevance, candidate.Rank.TopologySimilarity)
			}
		}
		if len(seenSource) < 2 {
			verified.SupportingScore *= .75
		}
		temporal := hypothesisTemporalConsistency(verified.VerifiedEvidenceIDs, allowed)
		verified.FinalScore = safety.Confidence(verified, temporal)
		if verified.ContradictionScore >= .50 {
			verified.Status = domain.HypothesisRefuted
		} else if verified.SupportingScore >= .65 && verified.ContradictionScore <= .20 {
			verified.Status = domain.HypothesisSupported
		} else {
			verified.Status = domain.HypothesisEvidenceSearching
		}
		verified.ConfidenceHistory = append(verified.ConfidenceHistory, domain.HypothesisConfidenceRecord{
			HypothesisID:        draft.ID,
			Sequence:            1,
			Score:               verified.FinalScore,
			SupportingScore:     verified.SupportingScore,
			ContradictionScore:  verified.ContradictionScore,
			CausalPathCoverage:  verified.CausalPathCoverage,
			HistoricalRelevance: verified.HistoricalRelevance,
			TopologyRelevance:   verified.TopologyRelevance,
			TemporalConsistency: temporal,
			AddedEvidenceIDs:    append([]string(nil), verified.VerifiedEvidenceIDs...),
			EvidenceSourceCount: len(seenSource),
			ComputedAt:          time.Now().UTC(),
		})
		rankingScore.WithLabelValues("causal_path_coverage").Observe(verified.CausalPathCoverage)
		rankingScore.WithLabelValues("contradiction").Observe(verified.ContradictionScore)
		rankingScore.WithLabelValues("root_cause_final").Observe(verified.FinalScore)
		out = append(out, verified)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].FinalScore == out[j].FinalScore {
			return out[i].Draft.ID < out[j].Draft.ID
		}
		return out[i].FinalScore > out[j].FinalScore
	})
	return out, nil
}

func hypothesisTemporalConsistency(ids []string, evidence map[string]domain.Evidence) float64 {
	if len(ids) == 0 {
		return 0
	}
	consistent := 0
	for _, id := range ids {
		item, ok := evidence[id]
		if !ok {
			continue
		}
		timestamp := item.Timestamp
		if timestamp.IsZero() {
			timestamp = item.ObservedAt
		}
		if timestamp.IsZero() || item.WindowStart.IsZero() || item.WindowEnd.IsZero() {
			continue
		}
		if !timestamp.Before(item.WindowStart) && !timestamp.After(item.WindowEnd) {
			consistent++
		}
	}
	return clamp(float64(consistent) / float64(len(ids)))
}

func normalizeCausalNodeID(value string) string {
	parts := strings.FieldsFunc(strings.ToLower(value), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	return strings.Join(parts, "_")
}

func expectedNodeObserved(expected, category string, observed map[string]bool, patterns []domain.CausalPattern) bool {
	normalized := normalizeCausalNodeID(expected)
	for _, pattern := range patterns {
		if pattern.Category != category {
			continue
		}
		for _, node := range pattern.Nodes {
			if !observed[node.ID] {
				continue
			}
			if node.ID == normalized || strings.Contains(normalized, node.ID) || strings.Contains(node.ID, normalized) {
				return true
			}
			for _, alias := range node.Match {
				alias = normalizeCausalNodeID(alias)
				if alias == normalized || strings.Contains(normalized, alias) || strings.Contains(alias, normalized) {
					return true
				}
			}
		}
	}
	return false
}

func sortCandidates(items []domain.RetrievalCandidate) {
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].RRFScore == items[j].RRFScore {
			return items[i].IncidentID < items[j].IncidentID
		}
		return items[i].RRFScore > items[j].RRFScore
	})
}
func sourceIn(v string, values ...string) bool {
	for _, candidate := range values {
		if strings.EqualFold(v, candidate) {
			return true
		}
	}
	return false
}
func hasSource(items []domain.Evidence, source string) bool {
	for _, item := range items {
		if strings.EqualFold(item.Source, source) {
			return true
		}
	}
	return false
}
func hasAnySource(items []domain.Evidence, sources ...string) bool {
	for _, item := range items {
		if sourceIn(item.Source, sources...) {
			return true
		}
	}
	return false
}
func stringify(v any) string {
	if v == nil {
		return ""
	}
	b, _ := json.Marshal(v)
	if string(b) == "null" {
		return ""
	}
	return string(b)
}
func clamp(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}
func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}
func tokenize(value string) []string {
	parts := strings.FieldsFunc(strings.ToLower(value), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' && r != '-' })
	seen := map[string]bool{}
	out := []string{}
	for _, p := range parts {
		if len(p) >= 3 && !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}
func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		if k != "" {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

func uniqueStrings(values []string) []string {
	seen := map[string]bool{}
	for _, value := range values {
		if value != "" {
			seen[value] = true
		}
	}
	return keys(seen)
}
func numeric(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	}
	return 0, false
}
func findString(m map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := m[key].(string); ok {
			return value
		}
	}
	return ""
}
func jaccard(a, b []string) float64 {
	left := map[string]bool{}
	for _, v := range a {
		left[v] = true
	}
	if len(left) == 0 && len(b) == 0 {
		return 0
	}
	intersection := 0
	union := len(left)
	for _, v := range b {
		if left[v] {
			intersection++
		} else {
			union++
		}
	}
	if union == 0 {
		return 0
	}
	return float64(intersection) / float64(union)
}
