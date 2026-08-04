package retrieval

import "context"

type Document struct {
	ID              string    `json:"id"`
	Service         string    `json:"service"`
	Namespace       string    `json:"namespace"`
	Category        string    `json:"category"`
	Template        string    `json:"template"`
	RootCause       string    `json:"root_cause"`
	Recovery        string    `json:"recovery"`
	Level           string    `json:"level,omitempty"`
	OccurrenceCount int       `json:"occurrence_count,omitempty"`
	Vector          []float32 `json:"-"`
	Score           float64   `json:"score,omitempty"`
}
type VectorStore interface {
	Upsert(context.Context, []Document) error
	Search(context.Context, []float32, map[string]string, int) ([]Document, error)
}
