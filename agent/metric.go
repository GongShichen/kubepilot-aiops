package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/kubepilot-aiops/kubepilot/internal/domain"
	"github.com/kubepilot-aiops/kubepilot/tools"
	"github.com/oklog/ulid/v2"
)

type MetricCollector struct{ Client *tools.PrometheusClient }

func (a MetricCollector) Collect(ctx context.Context, in *domain.Incident) ([]domain.Evidence, error) {
	queries := tools.MetricQueries(in.Namespace, in.Service)
	out := make([]domain.Evidence, 0, len(queries))
	names := make([]string, 0, len(queries))
	for name := range queries {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		q := queries[name]
		r, err := a.Client.Query(ctx, q, time.Time{})
		if err != nil {
			return out, fmt.Errorf("%s: %w", name, err)
		}
		var data any
		_ = json.Unmarshal(r.Result, &data)
		out = append(out, domain.Evidence{ID: ulid.Make().String(), Source: "prometheus", Kind: name, Summary: "Prometheus " + name + " query result", Data: map[string]any{"query": q, "result": data}, ObservedAt: time.Now().UTC()})
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
	out = append(out, domain.Evidence{ID: ulid.Make().String(), Source: "prometheus", Kind: "memory_trend", Summary: "Prometheus memory working-set trend over the incident observation window", Data: map[string]any{"query": rangeResult.Query, "start": start, "end": end, "step_seconds": 15, "result": trend}, ObservedAt: end})
	return out, nil
}
