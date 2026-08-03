package retrieval

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/kubepilot-aiops/kubepilot/tools"
)

type indexerCursor struct {
	mu     sync.Mutex
	cursor time.Time
}

func (c *indexerCursor) LoadCursor(context.Context, string) (time.Time, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cursor, nil
}
func (c *indexerCursor) SaveCursor(_ context.Context, _ string, value time.Time) error {
	c.mu.Lock()
	c.cursor = value
	c.mu.Unlock()
	return nil
}
func (*indexerCursor) Lock(context.Context, string, string, time.Duration) (bool, error) {
	return true, nil
}
func (*indexerCursor) Unlock(context.Context, string, string) error { return nil }
func (*indexerCursor) RefreshLock(context.Context, string, string, time.Duration) (bool, error) {
	return true, nil
}

type indexerLoki struct{ entries []tools.LokiEntry }

func (l indexerLoki) QueryRange(_ context.Context, _ string, start, end time.Time, _ int) ([]tools.LokiEntry, error) {
	var out []tools.LokiEntry
	for _, entry := range l.entries {
		if !entry.Timestamp.Before(start) && !entry.Timestamp.After(end) {
			out = append(out, entry)
		}
	}
	return out, nil
}

type indexerParser struct{ calls int }

func (p *indexerParser) ParseBatch(_ context.Context, records []LogRecord) ([]TemplateResult, error) {
	p.calls++
	result := make([]TemplateResult, len(records))
	for index, record := range records {
		result[index] = TemplateResult{RecordID: record.RecordID, ClusterID: 7, Template: "request <*> failed", OccurrenceCount: index + 1}
	}
	return result, nil
}

type indexerEmbedder struct{}

func (indexerEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i := range out {
		out[i] = []float32{1}
	}
	return out, nil
}

type indexerVectorStore struct{ docs []Document }

func (s *indexerVectorStore) Upsert(_ context.Context, docs []Document) error {
	s.docs = append(s.docs, docs...)
	return nil
}
func (*indexerVectorStore) Search(context.Context, []float32, map[string]string, int) ([]Document, error) {
	return nil, nil
}

type indexerMetadata struct{ records []LogTemplateRecord }

func (m *indexerMetadata) UpsertLogTemplates(_ context.Context, records []LogTemplateRecord) error {
	m.records = append(m.records, records...)
	return nil
}

func TestLogIndexerPersistsTemplateVectorAndSafeCursor(t *testing.T) {
	now := time.Now().UTC()
	cursors := &indexerCursor{cursor: now.Add(-time.Minute)}
	parser := &indexerParser{}
	vectors := &indexerVectorStore{}
	metadata := &indexerMetadata{}
	indexer := &LogIndexer{Loki: indexerLoki{entries: []tools.LokiEntry{{Timestamp: now.Add(-time.Second), Line: "request 42 failed", Labels: map[string]string{"namespace": "kubepilot-demo", "service": "gateway-service"}}}}, Parser: parser, Embedder: indexerEmbedder{}, Store: vectors, Metadata: metadata, Cursors: cursors, Namespaces: []string{"kubepilot-demo"}, BatchSize: 500}
	if err := indexer.IndexOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if parser.calls != 1 || len(vectors.docs) != 1 || len(metadata.records) != 1 {
		t.Fatalf("calls=%d docs=%d metadata=%d", parser.calls, len(vectors.docs), len(metadata.records))
	}
	if !cursors.cursor.Equal(now.Add(-time.Second)) {
		t.Fatalf("cursor=%s", cursors.cursor)
	}
	if err := indexer.IndexOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if parser.calls != 1 {
		t.Fatalf("already checkpointed log was reprocessed: calls=%d", parser.calls)
	}
}
