package agent

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/kubepilot-aiops/kubepilot/internal/domain"
	"github.com/kubepilot-aiops/kubepilot/retrieval"
	"github.com/kubepilot-aiops/kubepilot/tools"
)

type indexedLogSearch interface {
	Search(context.Context, string, string, string) ([]retrieval.Document, time.Duration, error)
}

// LogCollector only queries Loki and the continuously maintained template/vector
// index. Drain3 is deliberately absent from the Incident query path.
type LogCollector struct {
	Loki    *tools.LokiClient
	Indexed indexedLogSearch
}

func (a LogCollector) Collect(ctx context.Context, in *domain.Incident) ([]domain.Evidence, error) {
	end := time.Now().UTC()
	start := in.EvidenceStartAt
	if start.IsZero() {
		start = in.CreatedAt.Add(-5 * time.Minute)
	}
	entries, err := a.Loki.QueryRange(ctx, incidentLogQuery(in.Namespace, in.Service), start, end, 200)
	if err != nil {
		return nil, err
	}
	out := make([]domain.Evidence, 0, min(len(entries), 20)+5)
	queryParts := []string{in.Summary}
	for n, entry := range entries {
		if n < 20 {
			out = append(out, domain.Evidence{Source: "loki", Type: "log_entry", Timestamp: entry.Timestamp, WindowStart: start, WindowEnd: end, Namespace: in.Namespace, Service: in.Service, Resource: entry.Labels["pod"], Summary: entry.Line, Confidence: 1, TraceID: entry.Labels["trace_id"], Content: map[string]any{"level": entry.Labels["level"], "pod": entry.Labels["pod"]}})
		}
		if n < 10 {
			queryParts = append(queryParts, entry.Line)
		}
	}
	if a.Indexed == nil {
		return out, nil
	}
	docs, freshness, indexErr := a.Indexed.Search(ctx, strings.Join(queryParts, "\n"), in.Service, in.Namespace)
	if indexErr != nil {
		// Loki is the authoritative fallback when the optional index is stale or unavailable.
		return out, nil
	}
	stale := freshness == 0 || freshness > 30*time.Second
	for _, doc := range docs {
		out = append(out, domain.Evidence{Source: "loki", Type: "indexed_log_template", Timestamp: end, WindowStart: start, WindowEnd: end, Namespace: in.Namespace, Service: in.Service, Resource: in.Resource, Summary: doc.Template, Confidence: map[bool]float64{true: .5, false: .9}[stale], TemplateID: doc.ID, Content: map[string]any{"index_freshness_ms": freshness.Milliseconds(), "stale": stale, "index_metadata": doc.RootCause}})
	}
	return out, nil
}

func incidentLogQuery(namespace, service string) string {
	return fmt.Sprintf(`{namespace=%q,service=%q} |~ "(?i)(error|exception|timeout|killed|failed)"`, namespace, service)
}
