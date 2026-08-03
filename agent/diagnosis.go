package agent

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/kubepilot-aiops/kubepilot/internal/domain"
)

type modelEvidence struct {
	ID      string `json:"id"`
	Source  string `json:"source"`
	Kind    string `json:"kind"`
	Summary string `json:"summary"`
	Data    any    `json:"data,omitempty"`
}

func compactEvidence(items []domain.Evidence) []modelEvidence {
	items = append([]domain.Evidence(nil), items...)
	sort.SliceStable(items, func(i, j int) bool {
		left, right := evidencePriority(items[i]), evidencePriority(items[j])
		if left != right {
			return left < right
		}
		if items[i].Kind != items[j].Kind {
			return items[i].Kind < items[j].Kind
		}
		return items[i].ID < items[j].ID
	})
	out := make([]modelEvidence, 0, min(len(items), 24))
	counts := map[string]int{}
	for _, item := range items {
		key := item.Source + "/" + item.Kind
		limit := 2
		switch item.Kind {
		case "cpu", "cpu_throttling", "memory", "memory_trend", "qps", "error_rate", "p95_latency", "restarts", "deployment_availability", "workload_state", "historical_incident", "indexed_log_template":
			limit = 5
		case "log_template", "log_entry":
			limit = 5
		case "trace":
			limit = 3
		}
		if counts[key] >= limit || len(out) >= 24 {
			continue
		}
		counts[key]++
		data := any(item.Data)
		if data == nil {
			data = item.Content
		}
		encoded, err := json.Marshal(data)
		dataLimit := 2048
		if item.Kind == "workload_state" {
			dataLimit = 12 * 1024
		}
		if err == nil && len(encoded) > dataLimit {
			data = map[string]any{"truncated": true, "original_bytes": len(encoded), "json_preview": string(encoded[:dataLimit])}
		}
		out = append(out, modelEvidence{ID: item.ID, Source: item.Source, Kind: item.Kind, Summary: item.Summary, Data: data})
	}
	return out
}

func evidencePriority(item domain.Evidence) int {
	switch {
	case item.Kind == "workload_state":
		return 0
	case item.Kind == "log_template" || item.Kind == "indexed_log_template" || item.Kind == "log_entry":
		return 1
	case item.Kind == "trace":
		return 2
	case strings.HasSuffix(item.Kind, "_current"), strings.HasSuffix(item.Kind, "_trend"):
		return 3
	case item.Source == "prometheus":
		return 4
	case item.Kind == "historical_incident":
		return 5
	default:
		return 6
	}
}
