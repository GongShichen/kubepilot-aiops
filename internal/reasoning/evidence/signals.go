package evidence

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"

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
	return item
}

func metricAnomaly(kind string, facts map[string]any) float64 {
	current, hasCurrent := namedNumber(facts, "current", "current_value", "value")
	baseline, hasBaseline := namedNumber(facts, "baseline", "baseline_value")
	change, hasChange := namedNumber(facts, "change_rate", "change_ratio", "increase")
	if hasCurrent && hasBaseline {
		denominator := math.Max(math.Abs(baseline), 1e-9)
		change = math.Abs(current-baseline) / denominator
		hasChange = true
	}
	if hasChange {
		return clamp(change)
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
		if containsFailureObservation(facts) {
			return .9
		}
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
		return clamp(maximum / .20)
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
		// A working-set byte value needs a limit or a baseline before it says
		// anything about pressure.  The memory trend has several samples and
		// can be scored from its observed growth instead.
		if strings.Contains(kind, "trend") && len(numbers) >= 2 {
			minimum := minAbs(numbers)
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
			base = math.Max(base, .8)
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
	if networkPolicyDeniesSelectedTraffic(facts["network_policies"]) {
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
	switch typed := value.(type) {
	case []any:
		return len(typed) == 0
	case []string:
		return len(typed) == 0
	case nil:
		return true
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
