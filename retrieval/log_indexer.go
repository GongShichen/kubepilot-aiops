package retrieval

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/kubepilot-aiops/kubepilot/tools"
)

const LogIndexerCursorKey = "log-indexer:v1"

type CursorStore interface {
	LoadCursor(context.Context, string) (time.Time, error)
	SaveCursor(context.Context, string, time.Time) error
	Lock(context.Context, string, string, time.Duration) (bool, error)
	Unlock(context.Context, string, string) error
	RefreshLock(context.Context, string, string, time.Duration) (bool, error)
}

type LokiRangeReader interface {
	QueryRange(context.Context, string, time.Time, time.Time, int) ([]tools.LokiEntry, error)
}

type LogIndexer struct {
	Loki       LokiRangeReader
	Parser     Parser
	Embedder   EmbeddingClient
	Store      VectorStore
	Metadata   TemplateMetadataStore
	Cursors    CursorStore
	Namespaces []string
	PollEvery  time.Duration
	BatchSize  int
	Owner      string
}

type LogTemplateRecord struct {
	ID              string
	Namespace       string
	Service         string
	Template        string
	ClusterID       int
	OccurrenceCount int
	IndexedAt       time.Time
}

type TemplateMetadataStore interface {
	UpsertLogTemplates(context.Context, []LogTemplateRecord) error
}

func (i *LogIndexer) Run(ctx context.Context) error {
	if i.Loki == nil || i.Parser == nil || i.Embedder == nil || i.Store == nil || i.Cursors == nil {
		return fmt.Errorf("log indexer dependencies are incomplete")
	}
	if i.PollEvery <= 0 {
		i.PollEvery = 2 * time.Second
	}
	if i.BatchSize <= 0 || i.BatchSize > maxBatch {
		i.BatchSize = maxBatch
	}
	if i.Owner == "" {
		i.Owner = fmt.Sprintf("log-indexer-%d", time.Now().UnixNano())
	}
	locked, err := i.Cursors.Lock(ctx, LogIndexerCursorKey, i.Owner, 30*time.Second)
	if err != nil {
		return err
	}
	if !locked {
		return fmt.Errorf("another log indexer owns the cursor")
	}
	defer i.Cursors.Unlock(context.Background(), LogIndexerCursorKey, i.Owner) //nolint:errcheck
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	lockErrors := make(chan error, 1)
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-runCtx.Done():
				return
			case <-ticker.C:
				refreshed, refreshErr := i.Cursors.RefreshLock(runCtx, LogIndexerCursorKey, i.Owner, 30*time.Second)
				if refreshErr != nil || !refreshed {
					if refreshErr == nil {
						refreshErr = fmt.Errorf("log indexer lock ownership was lost")
					}
					select {
					case lockErrors <- refreshErr:
					default:
					}
					cancel()
					return
				}
			}
		}
	}()

	ticker := time.NewTicker(i.PollEvery)
	defer ticker.Stop()
	for {
		if err = i.IndexOnce(runCtx); err != nil && runCtx.Err() == nil {
			slog.Warn("log indexing iteration failed", "error", err)
		}
		select {
		case lockErr := <-lockErrors:
			return lockErr
		case <-runCtx.Done():
			return runCtx.Err()
		case <-ticker.C:
		}
	}
}

func (i *LogIndexer) IndexOnce(ctx context.Context) error {
	now := time.Now().UTC()
	cursor, err := i.Cursors.LoadCursor(ctx, LogIndexerCursorKey)
	if err != nil || cursor.IsZero() {
		cursor = now.Add(-5 * time.Minute)
	}
	var entries []tools.LokiEntry
	for _, namespace := range i.Namespaces {
		query := fmt.Sprintf("{namespace=%q}", namespace)
		items, queryErr := i.Loki.QueryRange(ctx, query, cursor.Add(time.Nanosecond), now, i.BatchSize)
		if queryErr != nil {
			return queryErr
		}
		entries = append(entries, items...)
	}
	if len(entries) == 0 {
		return i.Cursors.SaveCursor(ctx, LogIndexerCursorKey, now)
	}
	sort.Slice(entries, func(a, b int) bool { return entries[a].Timestamp.Before(entries[b].Timestamp) })
	for start := 0; start < len(entries); start += i.BatchSize {
		end := min(start+i.BatchSize, len(entries))
		if err = i.indexBatch(ctx, entries[start:end]); err != nil {
			return err
		}
		if err = i.Cursors.SaveCursor(ctx, LogIndexerCursorKey, entries[end-1].Timestamp); err != nil {
			return err
		}
	}
	return nil
}

func (i *LogIndexer) indexBatch(ctx context.Context, entries []tools.LokiEntry) error {
	records := make([]LogRecord, 0, len(entries))
	byID := make(map[string]LogRecord, len(entries))
	for _, entry := range entries {
		recordID := stableLogID(entry)
		record := LogRecord{RecordID: recordID, Timestamp: entry.Timestamp, Namespace: entry.Labels["namespace"], Service: entry.Labels["service"], Pod: entry.Labels["pod"], Level: entry.Labels["level"], TraceID: entry.Labels["trace_id"], Message: entry.Line}
		records = append(records, record)
		byID[recordID] = record
	}
	parsed, err := i.Parser.ParseBatch(ctx, records)
	if err != nil {
		return err
	}
	templates := make([]string, len(parsed))
	for n := range parsed {
		templates[n] = parsed[n].Template
	}
	vectors, err := i.Embedder.Embed(ctx, templates)
	if err != nil {
		return err
	}
	docs := make([]Document, 0, len(parsed))
	metadata := make([]LogTemplateRecord, 0, len(parsed))
	for n, item := range parsed {
		record := byID[item.RecordID]
		id := stableTemplateID(record.Namespace, record.Service, item.Template)
		docs = append(docs, Document{ID: id, Namespace: record.Namespace, Service: record.Service, Category: "log_template", Template: item.Template, RootCause: fmt.Sprintf("template_id=%d occurrence_count=%d indexed_at=%s", item.ClusterID, item.OccurrenceCount, record.Timestamp.UTC().Format(time.RFC3339Nano)), Vector: vectors[n]})
		metadata = append(metadata, LogTemplateRecord{ID: id, Namespace: record.Namespace, Service: record.Service, Template: item.Template, ClusterID: item.ClusterID, OccurrenceCount: item.OccurrenceCount, IndexedAt: record.Timestamp})
	}
	if err = i.Store.Upsert(ctx, docs); err != nil {
		return err
	}
	if i.Metadata != nil {
		return i.Metadata.UpsertLogTemplates(ctx, metadata)
	}
	return nil
}

type IndexedLogRetriever struct {
	Embedder EmbeddingClient
	Store    VectorStore
	Cursors  interface {
		LoadCursor(context.Context, string) (time.Time, error)
	}
	TopK int
}

func (r IndexedLogRetriever) Search(ctx context.Context, query, service, namespace string) ([]Document, time.Duration, error) {
	vectors, err := r.Embedder.Embed(ctx, []string{query})
	if err != nil {
		return nil, 0, err
	}
	topK := r.TopK
	if topK <= 0 {
		topK = 5
	}
	docs, err := r.Store.Search(ctx, vectors[0], map[string]string{"service": service, "namespace": namespace, "category": "log_template"}, topK)
	if err != nil {
		return nil, 0, err
	}
	var freshness time.Duration
	if r.Cursors != nil {
		if cursor, cursorErr := r.Cursors.LoadCursor(ctx, LogIndexerCursorKey); cursorErr == nil {
			freshness = time.Since(cursor)
		}
	}
	return docs, freshness, nil
}

func stableLogID(entry tools.LokiEntry) string {
	hash := sha256.Sum256([]byte(entry.Timestamp.UTC().Format(time.RFC3339Nano) + "\x00" + entry.Labels["namespace"] + "\x00" + entry.Labels["service"] + "\x00" + entry.Line))
	return hex.EncodeToString(hash[:])
}

func stableTemplateID(namespace, service, template string) string {
	hash := sha256.Sum256([]byte(strings.Join([]string{namespace, service, template}, "\x00")))
	return hex.EncodeToString(hash[:])
}
