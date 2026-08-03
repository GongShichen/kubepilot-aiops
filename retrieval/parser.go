package retrieval

import (
	"context"
	"time"
)

type LogRecord struct {
	RecordID  string    `json:"record_id"`
	Timestamp time.Time `json:"timestamp"`
	Service   string    `json:"service"`
	Namespace string    `json:"namespace"`
	Pod       string    `json:"pod"`
	Level     string    `json:"level"`
	TraceID   string    `json:"trace_id,omitempty"`
	Message   string    `json:"message"`
}
type TemplateResult struct {
	RecordID        string   `json:"record_id"`
	ClusterID       int      `json:"cluster_id"`
	Template        string   `json:"template"`
	Parameters      []string `json:"parameters"`
	OccurrenceCount int      `json:"occurrence_count"`
}
type Parser interface {
	ParseBatch(context.Context, []LogRecord) ([]TemplateResult, error)
}

type ParserStats struct {
	Batches    int `json:"batches"`
	Records    int `json:"records"`
	Attempts   int `json:"attempts"`
	Retries    int `json:"retries"`
	Reconnects int `json:"reconnects"`
}

type ParserStatsProvider interface {
	Stats() ParserStats
}
