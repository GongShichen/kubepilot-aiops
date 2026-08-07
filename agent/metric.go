package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/kubepilot-aiops/kubepilot/internal/domain"
	evidencenorm "github.com/kubepilot-aiops/kubepilot/internal/evidence"
	evidencesignals "github.com/kubepilot-aiops/kubepilot/internal/reasoning/evidence"
	"github.com/kubepilot-aiops/kubepilot/tools"
)

type MetricCollector struct{ Client *tools.PrometheusClient }

func (a MetricCollector) Collect(ctx context.Context, in *domain.Incident, request domain.EvidenceRequest) ([]domain.Evidence, error) {
	request, err := validateEvidenceRequest(in, request, "metric", nil)
	if err != nil {
		return nil, err
	}
	in = requestTargetIncident(in, request)
	queries := tools.MetricQueries(in.Namespace, in.Service)
	out := make([]domain.Evidence, 0, len(queries)*2)
	observations := make(map[string]float64, len(queries))
	names := make([]string, 0, len(queries))
	for name := range queries {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if !signalRequested(request.SignalKinds, name, "metric") {
			continue
		}
		q := queries[name]
		r, err := a.Client.Query(ctx, q, time.Time{})
		if err != nil {
			return out, fmt.Errorf("%s: %w", name, err)
		}
		var data any
		_ = json.Unmarshal(r.Result, &data)
		out = append(out, domain.Evidence{Source: "prometheus", Kind: name, Summary: "Prometheus " + name + " query result", Data: map[string]any{"query": q, "result": data}, ObservedAt: time.Now().UTC()})
		if value, ok := representativePrometheusValue(data); ok {
			observations[name] = value
		}
	}
	// A single Prometheus sample is often a raw rate, counter, or byte value
	// with no universal abnormality threshold. Pair each short observation with
	// its same-query window counterpart to surface a deterministic change signal
	// without teaching the agent any workload-specific expected value.
	for _, name := range names {
		if !strings.HasSuffix(name, "_current") || !signalRequested(request.SignalKinds, name, "metric") {
			continue
		}
		current, currentOK := observations[name]
		baseName := strings.TrimSuffix(name, "_current")
		baseline, baselineOK := observations[baseName]
		if !currentOK || !baselineOK {
			continue
		}
		denominator := math.Max(math.Abs(baseline), 1e-9)
		out = append(out, domain.Evidence{
			Source: "prometheus", Kind: baseName + "_change",
			Summary:    "Prometheus " + baseName + " current-to-window change",
			Data:       map[string]any{"baseline": baseline, "current": current, "change_rate": (current - baseline) / denominator, "normalization": metricNormalization(baseName)},
			ObservedAt: time.Now().UTC(),
		})
	}
	if len(request.SignalKinds) > 0 && !signalRequested(request.SignalKinds, "memory_trend", "metric") {
		return evidencenorm.Normalize(in, request, out), nil
	}
	end := time.Now().UTC()
	start := in.EvidenceStartAt
	if start.IsZero() || !start.Before(end) {
		start = end.Add(-5 * time.Minute)
	}
	rangeResult, err := a.Client.QueryRange(ctx, queries["memory"], start, end, 15*time.Second)
	if err != nil {
		return out, fmt.Errorf("memory_trend: %w", err)
	}
	var trend any
	_ = json.Unmarshal(rangeResult.Result, &trend)
	out = append(out, domain.Evidence{Source: "prometheus", Kind: "memory_trend", Summary: "Prometheus memory working-set trend over the incident observation window", Data: map[string]any{"query": rangeResult.Query, "start": start, "end": end, "step_seconds": 15, "result": trend}, ObservedAt: end})
	return evidencenorm.Normalize(in, request, out), nil
}

// metricNormalization documents the server query contract for derived change
// evidence. Parsers use it to distinguish a CPU-limit utilization ratio from
// a raw core-seconds rate, which cannot establish pressure on its own.
func metricNormalization(name string) string {
	if strings.EqualFold(name, "cpu") {
		return "ratio_to_cpu_limit"
	}
	return "window_relative_change"
}

func representativePrometheusValue(result any) (float64, bool) {
	values := evidencesignals.PrometheusMeasurementValues(result)
	if len(values) == 0 {
		return 0, false
	}
	value := values[0]
	for _, candidate := range values[1:] {
		if math.Abs(candidate) > math.Abs(value) {
			value = candidate
		}
	}
	return value, true
}

func signalRequested(requested []string, values ...string) bool {
	if len(requested) == 0 {
		return true
	}
	for _, request := range requested {
		for _, value := range values {
			if request == value || strings.Contains(request, value) || strings.Contains(value, request) {
				return true
			}
		}
	}
	return false
}
