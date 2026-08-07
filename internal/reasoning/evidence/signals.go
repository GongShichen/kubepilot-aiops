package evidence

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/kubepilot-aiops/kubepilot/internal/domain"
)

// AnalyzeEvidence derives anomaly strength from observed facts. Retrieval
// relevance is deliberately not an input: this score answers whether the
// observation is abnormal, not whether it is contextually close to an
// incident query.
func AnalyzeEvidence(item domain.Evidence) domain.Evidence {
	facts := item.Facts
	if len(facts) == 0 {
		facts = item.Content
	}
	if len(facts) == 0 {
		facts = item.Data
	}
	source := strings.ToLower(item.Source)
	kind := strings.ToLower(firstSignal(item.Type, item.Kind))
	score := 0.0
	switch source {
	case "prometheus", "metric":
		score = metricAnomaly(kind, facts)
	case "loki", "log":
		score = logAnomaly(item, facts)
	case "jaeger", "trace":
		score = traceAnomaly(facts)
	case "kubernetes", "topology":
		score = kubernetesAnomaly(facts)
	default:
		score = clamp(item.AnomalyScore)
	}
	item.AnomalyScore = clamp(score)
	item.CausalNodeIDs = appendUniqueSignal(item.CausalNodeIDs, "obs:"+item.ID)
	item.Signals = deriveSignals(item, facts)
	return item
}

// deriveSignals projects source-specific operational facts into a stable
// vocabulary. It deliberately records only parser-owned claims; summaries and
// model output are never used to invent a signal.
func deriveSignals(item domain.Evidence, facts map[string]any) []domain.EvidenceSignal {
	source := strings.ToLower(item.Source)
	kind := strings.ToLower(firstSignal(item.Type, item.Kind))
	category, signal, reliability, extraction := "observation", kind, .50, "deterministic_observation_parser"
	additional := []string(nil)
	switch source {
	case "prometheus", "metric":
		category, signal, reliability, extraction = metricSignal(kind), metricSignal(kind), .95, "prometheus_metric_parser"
		if _, baseline := namedNumber(facts, "baseline", "baseline_value"); !baseline && !strings.Contains(kind, "throttl") && !strings.Contains(kind, "availability") {
			reliability = .75
		}
		// A rising, limit-normalised memory range carries two distinct facts:
		// the observed pressure state and its growth mechanism.  Keeping both
		// server-derived signals lets the causal graph distinguish pre-OOM memory
		// exhaustion from a restart symptom without asking an LLM to infer it.
		if strings.Contains(kind, "memory") && strings.Contains(kind, "trend") {
			additional = append(additional, "memory_growth")
		}
	case "loki", "log":
		category, signal, reliability, extraction = "application", "log_error", .35, "loki_log_parser"
		level := strings.ToLower(fmt.Sprint(facts["level"]))
		switch level {
		case "critical", "fatal":
			reliability = .95
		case "error":
			reliability = .90
		case "warn", "warning":
			reliability = .65
		}
		additional = logSignalKinds(item, facts)
	case "jaeger", "trace":
		category, signal, reliability, extraction = "request", "trace_latency", .85, "jaeger_trace_parser"
		if nonEmptyString(facts["error_service"]) || nonEmptyString(facts["failed_operation"]) {
			signal = "trace_error"
			reliability = .95
			additional = failureSignalKinds(fmt.Sprint(facts["error_service"], " ", facts["failed_operation"]))
		}
	case "kubernetes", "topology":
		category, signal, reliability, extraction = "workload", "workload_health", .95, "kubernetes_state_parser"
		additional = kubernetesSignalKinds(facts)
		if containsString(additional, "network_policy_denial") {
			category, signal = "network", "network_policy_denial"
		}
	}
	if signal == "" {
		return nil
	}
	observed := item.ObservedAt
	if observed.IsZero() {
		observed = item.Timestamp
	}
	direction := "normal"
	if item.AnomalyScore > 0 {
		direction = "abnormal"
	}
	items := make([]domain.EvidenceSignal, 0, len(additional)+1)
	items = append(items, newSignal(item, source, category, signal, reliability, extraction, direction, observed))
	for _, additionalSignal := range additional {
		if additionalSignal == "" || additionalSignal == signal {
			continue
		}
		additionalCategory := signalCategory(additionalSignal)
		items = append(items, newSignal(item, source, additionalCategory, additionalSignal, reliability, extraction, signalDirection(additionalSignal, direction), observed))
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].Signal < items[j].Signal })
	return uniqueSignals(items)
}

// signalDirection keeps lifecycle/configuration observations distinct from an
// anomaly. A rollout transition is a real server fact that can participate in
// a causal path, but it is not itself proof of a bad rollout. The candidate
// engine requires a paired failed mechanism before it admits that pattern.
func signalDirection(signal, fallback string) string {
	if signal == "deployment_change" {
		return "observed"
	}
	return fallback
}

func newSignal(item domain.Evidence, source, category, signal string, reliability float64, extraction, direction string, observed time.Time) domain.EvidenceSignal {
	digest := sha256.Sum256([]byte(strings.Join([]string{item.ID, category, signal}, "|")))
	return domain.EvidenceSignal{
		ID: "signal-" + hex.EncodeToString(digest[:12]), EvidenceID: item.ID, Source: source,
		Category: category, Signal: signal, Value: item.AnomalyScore, Strength: item.AnomalyScore,
		Direction: direction, Reliability: reliability, Independence: 1, DiagnosticWeight: 1,
		Extraction: extraction, WindowStart: item.WindowStart, WindowEnd: item.WindowEnd,
		ObservedAt: observed, Namespace: item.Namespace, Service: item.Service, Resource: item.Resource,
	}
}

func uniqueSignals(items []domain.EvidenceSignal) []domain.EvidenceSignal {
	seen := make(map[string]bool, len(items))
	out := items[:0]
	for _, item := range items {
		if seen[item.Signal] {
			continue
		}
		seen[item.Signal] = true
		out = append(out, item)
	}
	return out
}

func signalCategory(signal string) string {
	switch signal {
	case "cpu_pressure", "cpu_throttling", "request_rate":
		return "resource"
	case "memory_pressure", "memory_growth", "oom_killed":
		return "memory"
	case "connection_pressure", "database_error":
		return "database"
	case "network_policy_configured", "network_policy_denial", "connection_refused", "connection_timeout", "network_unreachable", "endpoint_unavailable", "service_selector_mismatch", "configured_endpoint_unresolvable":
		return "network"
	case "database_endpoint_unavailable":
		return "database"
	case "image_pull_failure", "image_reference_unresolvable", "crash_loop", "probe_failure", "workload_unavailable", "deployment_change", "pod_restarts":
		return "workload"
	default:
		return "application"
	}
}

func metricSignal(kind string) string {
	switch {
	case strings.Contains(kind, "throttl"):
		return "cpu_throttling"
	case strings.Contains(kind, "cpu"), strings.Contains(kind, "saturation"):
		return "cpu_pressure"
	case strings.Contains(kind, "memory"):
		return "memory_pressure"
	case strings.Contains(kind, "latency"), strings.Contains(kind, "duration"):
		return "request_latency"
	case strings.Contains(kind, "error"):
		return "error_rate"
	case strings.Contains(kind, "restart"):
		return "pod_restarts"
	case strings.Contains(kind, "connection"):
		return "connection_pressure"
	case strings.Contains(kind, "qps"), strings.Contains(kind, "rps"), strings.Contains(kind, "throughput"), strings.Contains(kind, "request_rate"):
		return "request_rate"
	case strings.Contains(kind, "availability"), strings.HasSuffix(kind, "_up"):
		return "availability"
	default:
		return "metric_observation"
	}
}

// logSignalKinds extracts a small, parser-owned operational vocabulary from
// an actual log record. These are source facts, not causal conclusions: the
// causal engine consumes the resulting typed signals and never reinterprets
// arbitrary evidence prose itself.
func logSignalKinds(item domain.Evidence, facts map[string]any) []string {
	text := item.Summary + " " + fmt.Sprint(facts["error_signature"], " ", facts["message"], " ", facts["error"])
	return failureSignalKinds(text)
}

func failureSignalKinds(value string) []string {
	text := strings.ToLower(value)
	// A container image pull failure may embed a registry-level transport
	// detail such as "dial tcp".  Its diagnostic mechanism is nevertheless a
	// workload image acquisition failure, not an outbound dependency failure of
	// the application container.  Recognise that enclosing mechanism first so
	// a nested timeout cannot create an unrelated network/database candidate.
	if strings.Contains(text, "imagepull") || strings.Contains(text, "image pull") ||
		strings.Contains(text, "pulling image") || strings.Contains(text, "pull image") ||
		strings.Contains(text, "failed to pull") || strings.Contains(text, "failed pull") {
		out := []string{"image_pull_failure"}
		// These are Kubernetes/container-runtime diagnostics of the image
		// reference itself.  They are intentionally narrower than a generic
		// image-pull failure: a registry transport error alone remains an image
		// acquisition failure and must not be relabelled as an invalid reference.
		for _, marker := range []string{"failed to resolve reference", "manifest unknown", "no matching manifest", "invalid reference format", "image not found"} {
			if strings.Contains(text, marker) {
				out = append(out, "image_reference_unresolvable")
				break
			}
		}
		return out
	}
	var out []string
	switch {
	case strings.Contains(text, "oomkilled"), strings.Contains(text, "out of memory"):
		out = append(out, "oom_killed")
	case strings.Contains(text, "crashloop"):
		out = append(out, "crash_loop")
	case strings.Contains(text, "probe failed"), strings.Contains(text, "readiness probe"), strings.Contains(text, "liveness probe"):
		out = append(out, "probe_failure")
	case strings.Contains(text, "connection refused"):
		out = append(out, "connection_refused")
	case strings.Contains(text, "no route to host"), strings.Contains(text, "network is unreachable"):
		out = append(out, "network_unreachable")
	case strings.Contains(text, "connection timeout"), strings.Contains(text, "dial tcp"):
		out = append(out, "connection_timeout")
	case strings.Contains(text, "connection pool"), strings.Contains(text, "too many connections"):
		out = append(out, "connection_pressure")
	case strings.Contains(text, "sqlstate"), strings.Contains(text, "slow query"), strings.Contains(text, "database error"):
		out = append(out, "database_error")
	}
	return out
}

func kubernetesSignalKinds(facts map[string]any) []string {
	var out []string
	if networkPolicyEffectsDenySelectedTraffic(facts["network_policy_effects"]) {
		// An explicit evaluated effect is both a configuration fact and an
		// observed data-plane denial. They are separate causal roles but remain
		// one source for confidence aggregation.
		out = append(out, "network_policy_configured", "network_policy_denial")
	} else if networkPolicyDeniesSelectedTraffic(facts["network_policies"]) {
		out = append(out, "network_policy_denial")
	}
	if endpoints, exists := facts["endpoints"]; exists && isEmptyCollection(endpoints) {
		out = append(out, "endpoint_unavailable")
		if serviceSelectorMismatch(facts) {
			out = append(out, "service_selector_mismatch")
		}
		if databaseService(facts) {
			out = append(out, "database_endpoint_unavailable")
		}
	}
	if configuredEndpointUnresolvable(facts) {
		out = append(out, "configured_endpoint_unresolvable")
	}
	visitMaps(facts, func(key string, value any) {
		key = strings.ToLower(key)
		switch key {
		case "ready":
			if ready, ok := value.(bool); ok && !ready {
				out = append(out, "workload_unavailable")
			}
		case "restart_count":
			if count, ok := number(value); ok && count > 0 {
				out = append(out, "pod_restarts")
			}
		case "unavailable_replicas":
			if count, ok := number(value); ok && count > 0 {
				out = append(out, "workload_unavailable")
			}
		case "reason", "phase", "type", "message":
			text := fmt.Sprint(value)
			out = append(out, failureSignalKinds(text)...)
			if key == "reason" && (strings.EqualFold(strings.TrimSpace(text), "ScalingReplicaSet") || strings.EqualFold(strings.TrimSpace(text), "SuccessfulCreate")) {
				// Controller events establish a deployment transition. They do not
				// independently diagnose a regression: the causal graph still
				// requires a concrete failed pod mechanism.
				out = append(out, "deployment_change")
			}
		}
	})
	return uniqueStringValues(out)
}

// serviceSelectorMismatch derives a narrow, server-verifiable configuration
// failure. It requires all three facts: the selected Service has no endpoints,
// pods for the incident workload exist, and none satisfies the Service's own
// selector. It does not infer intent from names or text.
func serviceSelectorMismatch(facts map[string]any) bool {
	service, ok := facts["service"].(map[string]any)
	if !ok {
		return false
	}
	selector := stringMap(service["selector"])
	if len(selector) == 0 {
		return false
	}
	pods := mapItems(facts["pods"])
	if len(pods) == 0 {
		return false
	}
	for _, pod := range pods {
		labels := stringMap(pod["labels"])
		if labelsMatch(selector, labels) {
			return false
		}
	}
	return true
}

// configuredEndpointUnresolvable consumes the collector's structured result
// rather than treating arbitrary environment text as a failure. Only entries
// whose Kubernetes Service lookup was attempted and failed can create this
// signal.
func configuredEndpointUnresolvable(facts map[string]any) bool {
	for _, item := range mapItems(facts["configured_endpoint_resolution"]) {
		if strings.EqualFold(strings.TrimSpace(fmt.Sprint(item["status"])), "service_not_found") &&
			strings.TrimSpace(fmt.Sprint(item["host"])) != "" {
			return true
		}
	}
	return false
}

// databaseService classifies a Service from its declared protocol surface,
// never from its name. This lets an unavailable database endpoint use a
// database causal pattern without treating every missing endpoint as one.
func databaseService(facts map[string]any) bool {
	service, ok := facts["service"].(map[string]any)
	if !ok {
		return false
	}
	for _, port := range mapItems(service["ports"]) {
		name := strings.ToLower(strings.TrimSpace(fmt.Sprint(port["name"])))
		for _, marker := range []string{"mysql", "postgres", "mongo", "cassandra", "mariadb", "redis", "memcached", "elasticsearch"} {
			if strings.Contains(name, marker) {
				return true
			}
		}
		if value, ok := number(port["port"]); ok {
			switch int(value) {
			case 3306, 5432, 27017, 9042, 6379, 11211, 9200:
				return true
			}
		}
	}
	return false
}

func mapItems(value any) []map[string]any {
	switch typed := value.(type) {
	case []map[string]any:
		return typed
	case []any:
		out := make([]map[string]any, 0, len(typed))
		for _, item := range typed {
			if mapped, ok := item.(map[string]any); ok {
				out = append(out, mapped)
			}
		}
		return out
	default:
		// Collector facts can still contain client-go typed slices before their
		// API/audit serialization. Canonicalize only the local collection shape
		// so typed and JSON fixtures produce the same deterministic signal.
		raw, err := json.Marshal(value)
		if err != nil {
			return nil
		}
		var out []map[string]any
		if err = json.Unmarshal(raw, &out); err != nil {
			return nil
		}
		return out
	}
}

func stringMap(value any) map[string]string {
	out := map[string]string{}
	switch typed := value.(type) {
	case map[string]string:
		for key, item := range typed {
			out[key] = item
		}
	case map[string]any:
		for key, item := range typed {
			if text := strings.TrimSpace(fmt.Sprint(item)); text != "" && text != "<nil>" {
				out[key] = text
			}
		}
	}
	return out
}

func labelsMatch(selector, labels map[string]string) bool {
	if len(selector) == 0 {
		return false
	}
	for key, expected := range selector {
		if labels[key] != expected {
			return false
		}
	}
	return true
}

func uniqueStringValues(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func metricAnomaly(kind string, facts map[string]any) float64 {
	current, hasCurrent := namedNumber(facts, "current", "current_value", "value")
	baseline, hasBaseline := namedNumber(facts, "baseline", "baseline_value")
	change, hasChange := namedNumber(facts, "change_rate", "change_ratio", "increase")
	if hasCurrent && hasBaseline {
		denominator := math.Max(math.Abs(baseline), 1e-9)
		change = (current - baseline) / denominator
		hasChange = true
	}
	if hasChange {
		// The CPU collector contract supplies a ratio to the configured CPU
		// limit. A relative change of a raw core-seconds rate cannot establish
		// saturation: a negligible workload may have a large percentage change
		// from a near-zero baseline. Reject unnormalised CPU change evidence
		// rather than letting it activate a causal mechanism.
		if strings.Contains(kind, "cpu") && strings.HasSuffix(kind, "_change") {
			if !strings.EqualFold(fmt.Sprint(facts["normalization"]), "ratio_to_cpu_limit") {
				return 0
			}
			// Saturation is an absolute utilisation state, not a percentage
			// change from an arbitrarily small baseline. The paired current
			// measurement and this derived signal must therefore agree on the
			// same CPU-limit threshold.
			return clamp((current - .60) / .40)
		}
		return directionalMetricChange(kind, change)
	}
	// Prometheus represents an instant sample as [unix_timestamp, value] and a
	// range sample as [[unix_timestamp, value], ...].  A timestamp is metadata,
	// not a measurement.  Treating it as a metric value made otherwise healthy
	// observations appear maximally anomalous because a Unix timestamp dwarfs
	// every real signal.
	numbers := PrometheusMeasurementValues(facts["result"])
	if len(numbers) == 0 && hasCurrent {
		numbers = []float64{current}
	}
	if len(numbers) == 0 {
		return 0
	}
	maximum := maxAbs(numbers)
	switch {
	case strings.Contains(kind, "availability"), kind == "up", strings.HasSuffix(kind, "_up"):
		// Availability and Prometheus's conventional `up` are health signals:
		// one means healthy and zero means unavailable.
		return clamp(1 - maximum)
	case strings.Contains(kind, "restart"):
		return clamp(maximum / 3)
	case strings.Contains(kind, "goroutine"), strings.Contains(kind, "thread"), strings.Contains(kind, "worker"):
		// Runtime concurrency is meaningful only as a change from its own
		// baseline. A raw goroutine or thread count is intentionally neutral.
		return 0
	case strings.Contains(kind, "throttl"):
		// The collector reports the fraction of CFS scheduling periods that were
		// throttled, not raw throttle seconds. A sustained rate above 10% is a
		// measurable quota-pressure mechanism; short incidental bursts remain
		// neutral. This is intentionally independent of CPU utilisation: a
		// workload can be prevented from reaching its nominal limit precisely
		// because the kernel is denying scheduled periods.
		return clamp((maximum - .10) / .20)
	case strings.Contains(kind, "error"):
		return clamp(maximum / .10)
	case strings.Contains(kind, "latency"), strings.Contains(kind, "duration"):
		return clamp(maximum / 1.0)
	case strings.Contains(kind, "cpu"), strings.Contains(kind, "saturation"):
		// CPU ratios are normally in [0,1].  Raw core-seconds or a counter by
		// themselves have no universal saturation threshold, so leave values
		// outside that range neutral unless the collector supplied a baseline.
		if maximum > 1 {
			return 0
		}
		return clamp((maximum - .60) / .40)
	case strings.Contains(kind, "memory"):
		// The collector projects memory into a ratio to its declared limit. A
		// raw working-set byte value needs a limit before it says anything about
		// pressure. A low-utilisation trend is neutral even if it grew quickly
		// from a small baseline.
		if strings.Contains(kind, "trend") && len(numbers) >= 2 {
			minimum := minAbs(numbers)
			if maximum < .60 {
				return 0
			}
			if minimum > 0 {
				return clamp((maximum - minimum) / minimum)
			}
		}
		if maximum >= 0 && maximum <= 1 {
			return clamp((maximum - .60) / .40)
		}
		return 0
	default:
		// Rates and counters need an explicit baseline/expected range.  Do not
		// turn an arbitrary positive scalar into an anomaly.
		return 0
	}
}

// directionalMetricChange preserves the meaning of a change.  A decrease in
// latency, error rate, CPU pressure, or memory pressure is evidence of
// recovery, not an anomaly.  In particular, query metadata must never turn an
// empty Prometheus result into a synthetic failure merely because its metric
// name contains words such as "throttling" or "error".
func directionalMetricChange(kind string, change float64) float64 {
	kind = strings.ToLower(kind)
	if strings.Contains(kind, "availability") || kind == "up" || strings.HasSuffix(kind, "_up") {
		return clamp(-change)
	}
	return clamp(change)
}

func logAnomaly(item domain.Evidence, facts map[string]any) float64 {
	level := strings.ToLower(fmt.Sprint(facts["level"]))
	text := strings.ToLower(item.Summary + " " + fmt.Sprint(facts))
	base := 0.0
	switch level {
	case "critical", "fatal":
		base = 1
	case "error":
		base = .9
	case "warn", "warning":
		base = .6
	}
	for _, marker := range []string{"exception", "timeout", "killed", "failed", "failure", "oom", "refused", "unavailable", "throttl", "crashloop", "imagepull", "probe"} {
		if strings.Contains(text, marker) {
			// A marker without a structured severity is useful for retrieval but
			// cannot independently establish an incident mechanism.
			if level == "" {
				base = math.Max(base, .35)
			} else {
				base = math.Max(base, .8)
			}
		}
	}
	if count, ok := namedNumber(facts, "occurrence_count", "count"); ok && count > 1 {
		base = math.Max(base, clamp(math.Log2(count+1)/5))
	}
	return base
}

func traceAnomaly(facts map[string]any) float64 {
	if nonEmptyString(facts["error_service"]) || nonEmptyString(facts["failed_operation"]) {
		return 1
	}
	if containsFailureObservation(facts) {
		return .9
	}
	duration, ok := namedNumber(facts, "duration_micros", "duration_us")
	if !ok {
		return 0
	}
	// A normal, error-free trace contributes zero until its critical path is
	// observably above 250ms; service identity alone is never anomalous.
	return clamp((duration - 250_000) / 750_000)
}

func kubernetesAnomaly(facts map[string]any) float64 {
	score := 0.0
	visitMaps(facts, func(key string, value any) {
		key = strings.ToLower(key)
		switch key {
		case "ready":
			if ready, ok := value.(bool); ok && !ready {
				score = math.Max(score, 1)
			}
		case "restart_count", "unavailable_replicas":
			if number, ok := number(value); ok && number > 0 {
				score = math.Max(score, clamp(number/3))
			}
		case "available_replicas":
			if number, ok := number(value); ok && number == 0 {
				score = math.Max(score, .9)
			}
		case "reason", "phase", "type":
			text := strings.ToLower(fmt.Sprint(value))
			for _, marker := range []string{"warning", "backoff", "oomkilled", "failed", "crashloop", "pending", "imagepull"} {
				if strings.Contains(text, marker) {
					score = math.Max(score, .9)
				}
			}
		}
	})
	if endpoints, exists := facts["endpoints"]; exists && isEmptyCollection(endpoints) {
		score = math.Max(score, 1)
	}
	if serviceSelectorMismatch(facts) || configuredEndpointUnresolvable(facts) {
		// These are directly verified configuration mismatches, not an LLM
		// interpretation of an arbitrary manifest field.
		score = math.Max(score, .9)
	}
	if networkPolicyDeniesSelectedTraffic(facts["network_policies"]) || networkPolicyEffectsDenySelectedTraffic(facts["network_policy_effects"]) {
		score = math.Max(score, 1)
	}
	if containsFailureObservation(facts) {
		score = math.Max(score, .9)
	}
	return score
}

// networkPolicyDeniesSelectedTraffic recognizes Kubernetes's explicit
// isolation semantics: an Ingress/Egress policy with an empty rule list
// selects pods and denies all traffic in that direction.  It deliberately
// does not infer intent from a policy name or an unrelated configuration key.
func networkPolicyDeniesSelectedTraffic(value any) bool {
	policies, ok := value.([]any)
	if !ok {
		if typed, converted := value.([]map[string]any); converted {
			policies = make([]any, 0, len(typed))
			for _, policy := range typed {
				policies = append(policies, policy)
			}
		}
	}
	for _, value := range policies {
		policy, ok := value.(map[string]any)
		if !ok {
			continue
		}
		for _, policyType := range stringValues(policy["policy_types"]) {
			switch strings.ToLower(policyType) {
			case "egress":
				if rules, exists := policy["egress"]; exists && isEmptyCollection(rules) {
					return true
				}
			case "ingress":
				if rules, exists := policy["ingress"]; exists && isEmptyCollection(rules) {
					return true
				}
			}
		}
	}
	return false
}

// networkPolicyEffectsDenySelectedTraffic consumes the collector's explicit
// policy-effect projection. It does not infer a fault from policy names: a
// selected workload is anomalous only when Kubernetes reports a deny-all
// ingress/egress effect for that workload.
func networkPolicyEffectsDenySelectedTraffic(value any) bool {
	effects, ok := value.([]any)
	if !ok {
		if typed, converted := value.([]map[string]any); converted {
			effects = make([]any, 0, len(typed))
			for _, effect := range typed {
				effects = append(effects, effect)
			}
		}
	}
	for _, value := range effects {
		effect, ok := value.(map[string]any)
		if !ok {
			continue
		}
		if !strings.EqualFold(fmt.Sprint(effect["mode"]), "deny_all") {
			continue
		}
		direction := strings.ToLower(fmt.Sprint(effect["direction"]))
		if direction != "ingress" && direction != "egress" {
			continue
		}
		if len(stringValues(effect["selected_pods"])) > 0 {
			return true
		}
	}
	return false
}

func containsFailureObservation(value any) bool {
	// Scan scalar observations only. Serializing an entire Kubernetes object
	// would treat configuration keys such as `failureThreshold` as failures,
	// which turns a healthy workload into anomalous evidence.
	var contains func(any) bool
	contains = func(current any) bool {
		switch typed := current.(type) {
		case string:
			text := strings.ToLower(typed)
			for _, marker := range []string{"error", "exception", "timeout", "killed", "failed", "failure", "oom", "refused", "unavailable", "throttl", "crashloop", "imagepull", "probe failed"} {
				if strings.Contains(text, marker) {
					return true
				}
			}
		case []any:
			for _, item := range typed {
				if contains(item) {
					return true
				}
			}
		case []map[string]any:
			for _, item := range typed {
				if contains(item) {
					return true
				}
			}
		case []string:
			for _, item := range typed {
				if contains(item) {
					return true
				}
			}
		case map[string]any:
			for _, item := range typed {
				if contains(item) {
					return true
				}
			}
		}
		return false
	}
	return contains(value)
}

func namedNumber(values map[string]any, keys ...string) (float64, bool) {
	for _, key := range keys {
		if value, exists := values[key]; exists {
			if parsed, ok := number(value); ok {
				return parsed, true
			}
		}
	}
	return 0, false
}

func number(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case float32:
		return float64(typed), true
	case int:
		return float64(typed), true
	case int32:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case json.Number:
		parsed, err := typed.Float64()
		return parsed, err == nil
	case string:
		parsed, err := strconv.ParseFloat(typed, 64)
		return parsed, err == nil
	default:
		return 0, false
	}
}

func numericLeaves(value any) []float64 {
	var out []float64
	var walk func(any)
	walk = func(current any) {
		if value, ok := number(current); ok {
			out = append(out, value)
			return
		}
		switch typed := current.(type) {
		case []any:
			for _, item := range typed {
				walk(item)
			}
		case map[string]any:
			for key, item := range typed {
				if key != "timestamp" && key != "time" {
					walk(item)
				}
			}
		}
	}
	walk(value)
	return out
}

// PrometheusMeasurementValues extracts actual sample values while omitting the
// timestamp element that Prometheus carries alongside each value. Collectors
// use the same function when creating baseline/current derived observations.
func PrometheusMeasurementValues(value any) []float64 {
	var out []float64
	var walk func(any)
	walk = func(current any) {
		switch typed := current.(type) {
		case map[string]any:
			for key, item := range typed {
				switch strings.ToLower(key) {
				case "timestamp", "time":
					continue
				case "value", "values":
					walkPrometheusSamples(item, &out)
				default:
					walk(item)
				}
			}
		case []any:
			walkPrometheusSamples(typed, &out)
		default:
			if measurement, ok := number(typed); ok {
				out = append(out, measurement)
			}
		}
	}
	walk(value)
	return out
}

func walkPrometheusSamples(value any, out *[]float64) {
	if sample, ok := value.([]any); ok && len(sample) == 2 {
		if timestamp, timestampOK := number(sample[0]); timestampOK && looksLikeUnixTimestamp(timestamp) {
			if measurement, measurementOK := number(sample[1]); measurementOK {
				*out = append(*out, measurement)
				return
			}
		}
	}
	switch typed := value.(type) {
	case []any:
		for _, item := range typed {
			walkPrometheusSamples(item, out)
		}
	case map[string]any:
		for key, item := range typed {
			if key != "timestamp" && key != "time" {
				walkPrometheusSamples(item, out)
			}
		}
	default:
		if measurement, ok := number(typed); ok {
			*out = append(*out, measurement)
		}
	}
}

func looksLikeUnixTimestamp(value float64) bool {
	return value >= 946_684_800 && value <= 4_102_444_800
}

func stringValues(value any) []string {
	switch typed := value.(type) {
	case []string:
		return append([]string(nil), typed...)
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if text, ok := item.(string); ok {
				out = append(out, text)
			}
		}
		return out
	default:
		return nil
	}
}

func visitMaps(value any, visit func(string, any)) {
	var walk func(any)
	walk = func(current any) {
		switch typed := current.(type) {
		case map[string]any:
			for key, item := range typed {
				visit(key, item)
				walk(item)
			}
		case []any:
			for _, item := range typed {
				walk(item)
			}
		case []map[string]any:
			for _, item := range typed {
				walk(item)
			}
		}
	}
	walk(value)
}

func maxAbs(values []float64) float64 {
	maximum := 0.0
	for _, value := range values {
		maximum = math.Max(maximum, math.Abs(value))
	}
	return maximum
}

func minAbs(values []float64) float64 {
	minimum := math.Inf(1)
	for _, value := range values {
		minimum = math.Min(minimum, math.Abs(value))
	}
	if math.IsInf(minimum, 1) {
		return 0
	}
	return minimum
}

func nonEmptyString(value any) bool {
	return strings.TrimSpace(fmt.Sprint(value)) != "" && fmt.Sprint(value) != "<nil>"
}

func isEmptyCollection(value any) bool {
	if value == nil {
		return true
	}
	switch typed := value.(type) {
	case []any:
		return len(typed) == 0
	case []string:
		return len(typed) == 0
	}
	// Kubernetes client-go returns typed slices (for example
	// []v1.EndpointSubset), while JSON fixtures commonly use []any.  Both are
	// the same operational fact: a present-but-empty endpoint collection means
	// the selected Service has no ready backends.  Restrict the reflective
	// fallback to collection kinds so arbitrary scalar facts never become an
	// availability signal.
	valueOf := reflect.ValueOf(value)
	switch valueOf.Kind() {
	case reflect.Slice, reflect.Array, reflect.Map:
		return valueOf.Len() == 0
	default:
		return false
	}
}

func appendUniqueSignal(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func firstSignal(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return "unknown"
}
