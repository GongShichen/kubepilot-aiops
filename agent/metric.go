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

type MetricAgent struct{ Client *tools.PrometheusClient }

func (a MetricAgent) Collect(ctx context.Context, in *domain.Incident) ([]domain.Evidence, error) {
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
	return out, nil
}
