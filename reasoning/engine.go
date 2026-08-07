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
	evidencenorm "github.com/kubepilot-aiops/kubepilot/internal/evidence"
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
	// Evidence is the bounded, relevance-ranked model context. It preserves the
	// historical RankEvidence contract for callers that render or send evidence
	// to an LLM.
	Evidence []domain.Evidence `json:"evidence"`
	// RuntimeEvidence retains the complete normalized, ranked observation set
	// for deterministic signal, assertion, causal, and arbitration services.
	// Diagnosis must not lose a low-relevance-but-decisive observation merely
	// because the model context has a finite item or byte budget.
	RuntimeEvidence []domain.Evidence      `json:"runtime_evidence,omitempty"`
	Ledger          domain.DiagnosisLedger `json:"ledger"`
}

func (e *Engine) RankEvidence(incident *domain.Incident, input []domain.Evidence) (RankedEvidence, error) {
	if incident == nil {
		return RankedEvidence{}, fmt.Errorf("incident is required")
	}
	request := domain.EvidenceRequest{Source: "mixed", Targets: []domain.ResourceRef{{Namespace: incident.Namespace, Service: incident.Service, Resource: incident.Resource}}, WindowStart: incident.EvidenceStartAt, WindowEnd: time.Now().UTC()}
	input = evidencenorm.Normalize(incident, request, input)
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
		item = evidencepolicy.AnalyzeEvidence(item)
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
		// Keep the canonical facts in the runtime. The model receives a bounded
		// EvidenceView at its call boundary; replacing Facts here made a second
		// ranking pass re-parse a lossy preview and silently lose nested
		// Kubernetes states (for example ImagePullBackOff) and structured log
		// fields. Facts are the server-owned source of diagnostic truth, not a
		// model-context cache.
		selected[index] = evidenceForModelContext(selected[index])
	}
	if !hasSource(selected, "kubernetes") {
		return RankedEvidence{}, fmt.Errorf("ranked evidence is missing Kubernetes evidence")
	}
	if !hasAnySource(selected, "metric", "prometheus", "log", "loki", "trace", "jaeger") {
		return RankedEvidence{}, fmt.Errorf("ranked evidence is missing metric, log, or trace evidence")
	}
	// Account for the bounded model projection without mutating the runtime
	// Evidence. Every LLM-facing path uses the same view contract.
	retained, _ := json.Marshal(evidencenorm.Views(selected, e.config.ModelContextMaxBytes, 2048, e.config.ModelEvidenceMaxItems))
	ledger.EvidenceRetainedCount = len(selected)
	ledger.EvidenceRetainedBytes = len(retained)
	evidenceContextItems.WithLabelValues("after").Observe(float64(ledger.EvidenceRetainedCount))
	evidenceContextBytes.WithLabelValues("after").Observe(float64(ledger.EvidenceRetainedBytes))
	return RankedEvidence{Evidence: selected, RuntimeEvidence: items, Ledger: ledger}, nil
}

func evidenceForModelContext(item domain.Evidence) domain.Evidence {
	content := item.Facts
	if content == nil {
		content = item.Content
	}
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
	item.Facts = content
	item.Content = nil
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
	facts := evidenceFacts(e)
	text := strings.ToLower(e.Summary + " " + stringify(facts))
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
		observationText = strings.ToLower(stringify(observedResult(facts)))
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

func evidenceFacts(item domain.Evidence) map[string]any {
	if item.Facts != nil {
		return item.Facts
	}
	if item.Content != nil {
		return item.Content
	}
	return item.Data
}

func severityRarity(e domain.Evidence, text string) float64 {
	facts := evidenceFacts(e)
	level := strings.ToLower(findString(facts, "level", "severity"))
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
	if count, ok := numeric(firstValue(facts, "occurrence_count")); ok {
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

func requiredContextEvidenceIDs(items []domain.Evidence) map[string]bool {
	protected := map[string]bool{}
	for _, item := range items {
		if strings.EqualFold(item.Source, "kubernetes") {
			protected[item.ID] = true
			break
		}
	}
	for _, item := range items {
		if hasAnySource([]domain.Evidence{item}, "metric", "prometheus", "log", "loki", "trace", "jaeger") {
			protected[item.ID] = true
			break
		}
	}
	return protected
}

func fitEvidenceBytes(items []domain.Evidence, maxBytes int, protectedIDs map[string]bool) []domain.Evidence {
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
		if len(updated) >= largestBytes && len(result) > len(protectedIDs) {
			removable := largestRemovableEvidence(result, protectedIDs)
			if removable >= 0 {
				result = append(result[:removable], result[removable+1:]...)
				continue
			}
		}
		// All remaining records are mandatory source representatives. Returning
		// their already field-bounded form is safer than looping forever or
		// discarding the Kubernetes/telemetry evidence required by arbitration.
		if len(updated) >= largestBytes {
			return result
		}
	}
	return result
}

func largestRemovableEvidence(items []domain.Evidence, protectedIDs map[string]bool) int {
	largest := -1
	largestBytes := 0
	for index := range items {
		if protectedIDs[items[index].ID] {
			continue
		}
		bytes, _ := json.Marshal(items[index])
		if len(bytes) > largestBytes {
			largest, largestBytes = index, len(bytes)
		}
	}
	return largest
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
		facts := evidenceFacts(item)
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
		for _, service := range InferTopologyServices(item.Summary, stringify(facts)) {
			topology[service] = true
		}
		for _, id := range item.CausalNodeIDs {
			causal[id] = true
		}
		featureText := item.Summary + " " + stringify(facts)
		if sourceIn(item.Source, "metric", "prometheus") {
			featureText = item.Summary + " " + stringify(observedResult(facts))
		}
		for _, term := range tokenize(featureText) {
			terms[term] = true
		}
		for _, values := range []map[string]any{facts} {
			for key, value := range values {
				if key == "query" {
					continue
				}
				if number, ok := numeric(value); ok {
					f.Observed[key] = number
				}
			}
		}
		if revision := findString(facts, "revision", "deployment_revision"); revision != "" {
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
		facts := evidenceFacts(evidence)
		if value, ok := numeric(facts["latency_ms"]); ok {
			edge.LatencyMS = value
		}
		if value, ok := numeric(facts["error_rate"]); ok {
			edge.ErrorRate = value
		}
		edges[key] = edge
		paths[from+">"+to] = []string{from, to}
	}
	for _, item := range evidence {
		facts := evidenceFacts(item)
		text := strings.ToLower(item.Summary + " " + stringify(facts))
		observed := InferTopologyServices(item.Summary, stringify(facts))
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
			dependency := findString(facts, key)
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
	nodesBySignal := map[string][]domain.CausalNode{}
	for _, pattern := range patterns {
		if pattern.Status != "active" {
			continue
		}
		for _, node := range pattern.Nodes {
			for _, signal := range node.Signals {
				signal = strings.ToLower(strings.TrimSpace(signal))
				if signal != "" {
					nodesBySignal[signal] = append(nodesBySignal[signal], node)
				}
			}
		}
	}
	for index := range out {
		matched := map[string]bool{}
		for _, id := range out[index].CausalNodeIDs {
			matched[id] = true
		}
		// Causal nodes are attached only from anomalous server signals, except
		// that a server-observed lifecycle/configuration transition can establish
		// a declared cause node. Such a transition remains neutral for evidence
		// support and must be paired with a failed mechanism by the pattern's
		// admission contract.
		// Evidence summaries, facts blobs and model prose are intentionally not
		// consulted here: text similarity can turn a shared symptom or service
		// name into a fabricated causal mechanism.
		for _, signal := range out[index].Signals {
			for _, node := range nodesBySignal[strings.ToLower(strings.TrimSpace(signal.Signal))] {
				if signal.Direction != "abnormal" && !(signal.Direction == "observed" && strings.EqualFold(strings.TrimSpace(node.Type), "cause")) {
					continue
				}
				if node.Source != "" && !strings.EqualFold(node.Source, out[index].Source) {
					continue
				}
				matched[node.ID] = true
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

func (e *Engine) MatchCausalPatterns(_ domain.IncidentFeatures, evidence []domain.Evidence, patterns []domain.CausalPattern) []domain.CausalPattern {
	observed := map[string]bool{}
	for _, item := range evidence {
		if item.AnomalyScore <= 0 {
			continue
		}
		for _, nodeID := range item.CausalNodeIDs {
			observed[nodeID] = true
		}
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
			if observed[node.ID] {
				hit++
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

func (e *Engine) VerifyHypotheses(drafts []domain.HypothesisDraft, evidence []domain.Evidence, candidates []domain.RetrievalCandidate, patterns []domain.CausalPattern, assertionSets ...[]domain.StateAssertion) ([]domain.VerifiedHypothesis, error) {
	evidence = e.AnnotateCausalNodes(evidence, patterns)
	allowed := map[string]domain.Evidence{}
	for _, item := range evidence {
		allowed[item.ID] = item
	}
	out := make([]domain.VerifiedHypothesis, 0, len(drafts))
	var assertions []domain.StateAssertion
	if len(assertionSets) > 0 {
		assertions = assertionSets[0]
	}
	for _, draft := range drafts {
		if len(draft.SupportingEvidenceIDs) == 0 {
			return nil, fmt.Errorf("hypothesis %s has no supporting evidence", draft.ID)
		}
		verified := domain.VerifiedHypothesis{Draft: draft}
		seenSource := map[string]bool{}
		sourceSupport := map[string]float64{}
		supportingIDs := append([]string(nil), draft.SupportingEvidenceIDs...)
		// Kubernetes scope evidence is server-attributed rather than model
		// asserted. It establishes the exact workload/topology for a candidate
		// but is deliberately not treated as anomalous support unless the
		// hypothesis also cites a matching abnormal Kubernetes observation.
		for _, id := range scopedKubernetesEvidenceIDs(draft, supportingIDs, allowed) {
			if !containsEvidenceID(supportingIDs, id) {
				supportingIDs = append(supportingIDs, id)
			}
		}
		expectedNodeIDs := draft.ExpectedCausalNodeIDs
		if len(expectedNodeIDs) == 0 {
			// Compatibility for pre-node-ID clients: legacy natural-language paths
			// are not matched. The server deterministically projects cited current
			// observations to canonical nodes instead.
			for _, evidenceID := range draft.SupportingEvidenceIDs {
				expectedNodeIDs = append(expectedNodeIDs, "obs:"+evidenceID)
			}
			verified.Draft.ExpectedCausalNodeIDs = append([]string(nil), expectedNodeIDs...)
			verified.Draft.ExpectedCausalPath = append([]string(nil), expectedNodeIDs...)
		}
		for _, id := range supportingIDs {
			item, ok := allowed[id]
			if !ok {
				return nil, fmt.Errorf("hypothesis %s references unknown or expired evidence ID %s", draft.ID, id)
			}
			verified.VerifiedEvidenceIDs = append(verified.VerifiedEvidenceIDs, id)
			seenSource[item.Source] = true
			if evidenceSupportsExpectedNode(item, expectedNodeIDs) {
				// Supporting confidence is derived from the strongest *causally
				// relevant signal* for each source, rather than the aggregate
				// quality of an evidence envelope. A collector may put unrelated
				// normal and abnormal observations into the same envelope; using
				// its maximum quality either dilutes a decisive signal or lets an
				// unrelated signal support a hypothesis. Legacy evidence without
				// structured signals keeps the envelope-level fallback below.
				support := causalSignalSupport(item, expectedNodeIDs, patterns)
				sourceSupport[item.Source] = math.Max(sourceSupport[item.Source], support)
			}
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
		// A source contributes at most its strongest causally relevant signal.
		// Combine distinct sources as independent confirmations rather than
		// averaging them: weak but relevant corroboration must not reduce the
		// confidence supplied by a strong independent observation. This is the
		// bounded noisy-OR of source-level evidence, so repeated signals from a
		// single source cannot inflate confidence.
		for _, score := range sourceSupport {
			if score > 0 {
				verified.SupportingScore = 1 - (1-verified.SupportingScore)*(1-clamp(score))
			}
		}
		for _, id := range draft.ContradictingEvidenceIDs {
			if _, ok := allowed[id]; !ok {
				return nil, fmt.Errorf("hypothesis %s references unknown or expired contradiction evidence ID %s", draft.ID, id)
			}
			verified.ContradictionScore += .25
		}
		verified.ContradictionScore = clamp(verified.ContradictionScore)
		coverage, missing, coverageErr := canonicalCausalCoverage(expectedNodeIDs, evidence, patterns)
		if coverageErr != nil {
			return nil, fmt.Errorf("hypothesis %s causal nodes: %w", draft.ID, coverageErr)
		}
		if draft.RequireCausalMechanism {
			// A deterministic root-cause candidate must observe the decisive part
			// of its causal pattern.  Previously this checked only that the
			// candidate *named* a cause/mechanism node in its expected path.  That
			// let correlated symptoms (for example request errors plus short CFS
			// throttling bursts) satisfy a CPU-saturation path without an observed
			// saturation mechanism.  Pattern membership is not evidence: require
			// a server-annotated mechanism node when the pattern defines one, or a
			// server-annotated cause node for a deliberately mechanism-free
			// pattern.
			if requiredNodes := missingObservedDecisiveCausalNodes(expectedNodeIDs, evidence, patterns); len(requiredNodes) > 0 {
				coverage = 0
				missing = uniqueStrings(append(missing, requiredNodes...))
			}
		}
		verified.CausalPathCoverage = coverage
		verified.MissingCausalNodes = missing
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
		if len(assertions) > 0 {
			verified.ObservationCoverage = observationCoverage(draft, assertions)
		} else {
			// Compatibility for callers that predate StateAssertion. New
			// KubePilot flows always pass assertions.
			verified.ObservationCoverage = verified.CausalPathCoverage
		}
		temporal := hypothesisTemporalConsistency(verified.VerifiedEvidenceIDs, allowed)
		verified.ObjectiveScore = safety.Confidence(verified, temporal)
		verified.FinalScore = verified.ObjectiveScore
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
			ObjectiveScore:      verified.ObjectiveScore,
			ObservationCoverage: verified.ObservationCoverage,
			ModelPrior:          draft.PriorProbability,
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

func scopedKubernetesEvidenceIDs(draft domain.HypothesisDraft, supporting []string, evidence map[string]domain.Evidence) []string {
	namespace, service, resource := "", draft.Service, draft.Resource
	for _, id := range supporting {
		item, ok := evidence[id]
		if !ok {
			continue
		}
		if namespace == "" {
			namespace = item.Namespace
		}
		if service == "" {
			service = item.Service
		}
		if resource == "" {
			resource = item.Resource
		}
	}
	var candidates []domain.Evidence
	for _, item := range evidence {
		if !strings.EqualFold(item.Source, "kubernetes") || !sameEvidenceScope(item, namespace, service, resource) {
			continue
		}
		candidates = append(candidates, item)
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].RelevanceScore == candidates[j].RelevanceScore {
			return candidates[i].ID < candidates[j].ID
		}
		return candidates[i].RelevanceScore > candidates[j].RelevanceScore
	})
	if len(candidates) == 0 {
		return nil
	}
	return []string{candidates[0].ID}
}

func sameEvidenceScope(item domain.Evidence, namespace, service, resource string) bool {
	if namespace != "" && item.Namespace != "" && item.Namespace != namespace {
		return false
	}
	if service != "" && item.Service != "" && item.Service != service {
		return false
	}
	if resource != "" && item.Resource != "" && item.Resource != resource {
		return false
	}
	return true
}

func containsEvidenceID(ids []string, target string) bool {
	for _, id := range ids {
		if id == target {
			return true
		}
	}
	return false
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

func evidenceSupportsExpectedNode(item domain.Evidence, expected []string) bool {
	allowed := map[string]bool{"obs:" + item.ID: true}
	for _, nodeID := range item.CausalNodeIDs {
		allowed[nodeID] = true
	}
	for _, nodeID := range expected {
		if allowed[nodeID] {
			return true
		}
	}
	return false
}

// causalSignalSupport returns the quality of the strongest abnormal signal in
// an evidence item that maps to a node on the candidate's server-owned causal
// path. It deliberately does not use evidence summary text, Facts, or model
// prose. When a legacy pattern/evidence record has no typed node-signal mapping
// it falls back to the existing envelope quality so older callers retain their
// bounded behaviour.
func causalSignalSupport(item domain.Evidence, expected []string, patterns []domain.CausalPattern) float64 {
	expectedNodes := map[string]bool{}
	for _, nodeID := range expected {
		expectedNodes[nodeID] = true
	}
	nodeSignals := map[string]map[string]bool{}
	for _, pattern := range patterns {
		if pattern.Status != "active" {
			continue
		}
		for _, node := range pattern.Nodes {
			if !expectedNodes[node.ID] {
				continue
			}
			if node.Source != "" && !strings.EqualFold(node.Source, item.Source) {
				continue
			}
			for _, value := range node.Signals {
				value = strings.ToLower(strings.TrimSpace(value))
				if value == "" {
					continue
				}
				if nodeSignals[node.ID] == nil {
					nodeSignals[node.ID] = map[string]bool{}
				}
				nodeSignals[node.ID][value] = true
			}
		}
	}

	// An obs:<evidence-id> path is the pre-signal compatibility contract. It
	// represents this whole observation, but only an abnormal typed signal may
	// contribute support.
	legacyObservation := expectedNodes["obs:"+item.ID]
	if len(nodeSignals) == 0 && !legacyObservation {
		if item.QualityScore > 0 {
			return clamp(item.QualityScore)
		}
		return clamp(item.AnomalyScore)
	}

	best := 0.0
	for _, signal := range item.Signals {
		if signal.Direction != "abnormal" {
			continue
		}
		name := strings.ToLower(strings.TrimSpace(signal.Signal))
		matches := legacyObservation
		if !matches {
			for _, values := range nodeSignals {
				if values[name] {
					matches = true
					break
				}
			}
		}
		if !matches {
			continue
		}
		reliability := signal.Reliability
		if reliability <= 0 {
			reliability = 1
		}
		independence := signal.Independence
		if independence <= 0 {
			independence = 1
		}
		temporal := signal.TemporalAlignment
		if temporal <= 0 {
			temporal = 1
		}
		quality := clamp(signal.Strength * reliability * independence * temporal)
		best = math.Max(best, quality)
	}
	if best == 0 && legacyObservation {
		if item.QualityScore > 0 {
			return clamp(item.QualityScore)
		}
		return clamp(item.AnomalyScore)
	}
	return best
}

// canonicalCausalCoverage accepts only IDs supplied by the server. Coverage
// requires an evidence support relation; pattern membership or textual
// similarity alone never marks a node as observed.
func canonicalCausalCoverage(expected []string, evidence []domain.Evidence, patterns []domain.CausalPattern) (float64, []string, error) {
	if len(expected) == 0 {
		return 0, nil, fmt.Errorf("at least one causal node ID is required")
	}
	patternNodes := map[string]bool{}
	for _, pattern := range patterns {
		if pattern.Status != "active" {
			continue
		}
		for _, node := range pattern.Nodes {
			patternNodes[node.ID] = true
		}
	}
	allowed := map[string]bool{}
	observed := map[string]bool{}
	edges := map[string]bool{}
	for _, item := range evidence {
		observationID := "obs:" + item.ID
		allowed[observationID] = true
		observed[observationID] = true
		for _, nodeID := range item.CausalNodeIDs {
			if patternNodes[nodeID] {
				allowed[nodeID] = true
				observed[nodeID] = true
			}
		}
	}
	for _, pattern := range patterns {
		if pattern.Status != "active" {
			continue
		}
		for _, node := range pattern.Nodes {
			allowed[node.ID] = true
			if len(node.SourceEvidenceIDs) > 0 {
				for _, evidenceID := range node.SourceEvidenceIDs {
					if observed["obs:"+evidenceID] {
						observed[node.ID] = true
					}
				}
			}
		}
		for _, edge := range pattern.Edges {
			edges[edge.From+"\x00"+edge.To] = true
		}
	}
	missing := make([]string, 0)
	covered := 0
	for _, nodeID := range expected {
		if !allowed[nodeID] {
			return 0, nil, fmt.Errorf("unknown node ID %q", nodeID)
		}
		if observed[nodeID] {
			covered++
		} else {
			missing = append(missing, nodeID)
		}
	}
	nodeCoverage := float64(covered) / float64(len(expected))
	if len(expected) == 1 {
		return nodeCoverage, missing, nil
	}
	validEdges := 0
	for index := 0; index < len(expected)-1; index++ {
		from, to := expected[index], expected[index+1]
		if edges[from+"\x00"+to] {
			validEdges++
		}
	}
	pathCoverage := float64(validEdges) / float64(len(expected)-1)
	return clamp(nodeCoverage * pathCoverage), missing, nil
}

// missingObservedDecisiveCausalNodes returns the causal admission nodes a
// deterministic candidate has not actually observed. A downstream mechanism
// can be a shared consequence of unrelated patterns, so only a root cause or
// a mechanism directly caused by a root cause may establish a candidate.
// This check consumes typed server signals rather than prose or static graph
// membership.
func missingObservedDecisiveCausalNodes(expected []string, evidence []domain.Evidence, patterns []domain.CausalPattern) []string {
	expectedSet := map[string]bool{}
	for _, nodeID := range expected {
		expectedSet[nodeID] = true
	}
	observed := map[string]bool{}
	directMechanisms := map[string]bool{}
	directCauses := map[string]bool{}
	for _, pattern := range patterns {
		if pattern.Status != "active" {
			continue
		}
		for _, node := range pattern.Nodes {
			if !expectedSet[node.ID] || !candidateAdmissionNodes(pattern)[node.ID] {
				continue
			}
			if strings.EqualFold(strings.TrimSpace(node.Type), "mechanism") {
				directMechanisms[node.ID] = true
			} else if strings.EqualFold(strings.TrimSpace(node.Type), "cause") {
				directCauses[node.ID] = true
			}
			for _, item := range evidence {
				if evidenceMatchesCausalNode(item, node) {
					observed[node.ID] = true
				}
			}
		}
	}
	required := directCauses
	if len(directMechanisms) > 0 {
		required = directMechanisms
	}
	for nodeID := range required {
		if observed[nodeID] {
			return nil
		}
	}
	return keys(required)
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
