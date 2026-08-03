package retrieval

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/kubepilot-aiops/kubepilot/internal/domain"
	"github.com/oklog/ulid/v2"
)

type EmbeddingClient interface {
	Embed(context.Context, []string) ([][]float32, error)
}

type HistoricalEvidence struct {
	Embedder EmbeddingClient
	Store    VectorStore
	TopK     int
}

func (h HistoricalEvidence) Collect(ctx context.Context, in *domain.Incident) ([]domain.Evidence, error) {
	parts := historicalQueryParts(in)
	vectors, err := h.Embedder.Embed(ctx, []string{strings.Join(parts, "\n")})
	if err != nil {
		return nil, err
	}
	topK := h.TopK
	if topK <= 0 {
		topK = 5
	}
	docs, err := h.Store.Search(ctx, vectors[0], map[string]string{"service": in.Service, "namespace": in.Namespace}, topK)
	if err != nil {
		return nil, err
	}
	out := make([]domain.Evidence, 0, len(docs))
	for _, doc := range docs {
		out = append(out, domain.Evidence{ID: ulid.Make().String(), Source: "historical", Kind: "historical_incident", Summary: doc.RootCause, Data: map[string]any{"document_id": doc.ID, "category": doc.Category, "template": doc.Template, "recovery": doc.Recovery}, ObservedAt: time.Now().UTC()})
	}
	return out, nil
}

func historicalQueryParts(in *domain.Incident) []string {
	parts := []string{in.Summary, "service " + in.Service, "resource " + in.Resource}
	for _, item := range in.Evidence {
		switch item.Kind {
		case "log_template":
			parts = append(parts, "observed error log: "+item.Summary)
		case "trace":
			if encoded, err := json.Marshal(item.Data); err == nil {
				parts = append(parts, "observed trace: "+string(encoded))
			}
		case "workload_state":
			parts = append(parts, "observed Kubernetes workload state: "+item.Summary)
		case "memory_trend":
			parts = append(parts, describeMemoryTrend(item.Data))
		}
	}
	return parts
}

func describeMemoryTrend(data map[string]any) string {
	encoded, err := json.Marshal(data["result"])
	if err != nil {
		return "memory working-set trend was collected"
	}
	var series []struct {
		Values [][]any `json:"values"`
	}
	if json.Unmarshal(encoded, &series) != nil || len(series) == 0 || len(series[0].Values) < 2 {
		return "memory working-set trend was collected"
	}
	value := func(point []any) (float64, bool) {
		if len(point) < 2 {
			return 0, false
		}
		switch raw := point[1].(type) {
		case string:
			n, parseErr := strconv.ParseFloat(raw, 64)
			return n, parseErr == nil
		case float64:
			return raw, true
		default:
			return 0, false
		}
	}
	first, firstOK := value(series[0].Values[0])
	last, lastOK := value(series[0].Values[len(series[0].Values)-1])
	if !firstOK || !lastOK {
		return "memory working-set trend was collected"
	}
	direction := "remained stable"
	if last > first*1.2 {
		direction = "increased materially"
		monotonic := true
		previous := first
		for _, point := range series[0].Values[1:] {
			current, ok := value(point)
			if !ok || current < previous*0.98 {
				monotonic = false
				break
			}
			previous = current
		}
		if monotonic {
			direction = "increased monotonically"
		}
	} else if first > 0 && last < first*0.8 {
		direction = "decreased"
	}
	return fmt.Sprintf("memory working set %s from %.0f bytes to %.0f bytes during the incident window", direction, first, last)
}
