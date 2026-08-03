package agent

import (
	"context"
	"fmt"
	"time"

	"github.com/kubepilot-aiops/kubepilot/internal/domain"
	"github.com/kubepilot-aiops/kubepilot/retrieval"
	"github.com/kubepilot-aiops/kubepilot/tools"
	"github.com/oklog/ulid/v2"
)

type LogAgent struct {
	Loki   *tools.LokiClient
	Parser retrieval.Parser
}

func (a LogAgent) Collect(ctx context.Context, in *domain.Incident) ([]domain.Evidence, error) {
	end := time.Now().UTC()
	start := in.EvidenceStartAt
	if start.IsZero() {
		start = in.CreatedAt.Add(-5 * time.Minute)
	}
	query := incidentLogQuery(in.Namespace, in.Service)
	entries, err := a.Loki.QueryRange(ctx, query, start, end, 200)
	if err != nil {
		return nil, err
	}
	if len(entries) == 0 {
		return []domain.Evidence{}, nil
	}
	records := make([]retrieval.LogRecord, 0, len(entries))
	for _, e := range entries {
		records = append(records, retrieval.LogRecord{RecordID: ulid.Make().String(), Timestamp: e.Timestamp, Service: in.Service, Namespace: in.Namespace, Pod: e.Labels["pod"], Level: e.Labels["level"], TraceID: e.Labels["trace_id"], Message: e.Line})
	}
	templates, err := a.Parser.ParseBatch(ctx, records)
	if err != nil {
		return nil, err
	}
	out := make([]domain.Evidence, 0, len(templates))
	for _, t := range templates {
		out = append(out, domain.Evidence{ID: ulid.Make().String(), Source: "loki+drain3", Kind: "log_template", Summary: t.Template, Data: map[string]any{"cluster_id": t.ClusterID, "count": t.OccurrenceCount, "parameters": t.Parameters}, ObservedAt: end})
	}
	return out, nil
}

func incidentLogQuery(namespace, service string) string {
	return fmt.Sprintf(`{namespace=%q,service=%q,benchmark_dataset!="retrieval"} |~ "(?i)(error|exception|timeout|killed|failed)"`, namespace, service)
}
